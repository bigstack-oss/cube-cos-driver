package orchestrator

import (
	"context"
	"fmt"
	"time"

	goipmi "github.com/bougou/go-ipmi"
)

// IPMIExecutor drives real hardware: power/bootdev over IPMI lanplus, and
// install-stage observation via an injected Observer (pxeserver logs). Never
// used in CI — see FakeExecutor.
type IPMIExecutor struct {
	// Observer reports install progress by watching the pxeserver for a
	// node's MACs; nil disables observation (stays at netbooting).
	Observer Observer
}

// Observer reports the furthest install stage seen for a node's MACs.
type Observer interface {
	Stage(ctx context.Context, macs []string) (Stage, error)
}

func dial(ctx context.Context, n Node) (*goipmi.Client, error) {
	host, port := splitHostPort(n.BMCAddress)
	client, err := goipmi.NewClient(host, port, n.BMCUser, n.BMCPass)
	if err != nil {
		return nil, err
	}
	client = client.WithInterface(goipmi.InterfaceLanplus)
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

func splitHostPort(addr string) (string, int) {
	port := 623
	host := addr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			p := 0
			if _, err := fmt.Sscanf(addr[i+1:], "%d", &p); err == nil && p > 0 {
				host = addr[:i]
				port = p
			}
			break
		}
	}
	return host, port
}

func (e IPMIExecutor) Preflight(ctx context.Context, n Node) error {
	client, err := dial(ctx, n)
	if err != nil {
		return fmt.Errorf("bmc unreachable: %w", err)
	}
	defer client.Close(ctx)
	if _, err := client.GetChassisStatus(ctx); err != nil {
		return fmt.Errorf("chassis status: %w", err)
	}
	return nil
}

// setBootFlags sends Set System Boot Options (param 5, boot flags) as a raw IPMI
// command: valid + persistent + EFI boot type + the given device selector
// (0x04 Force-PXE, 0x08 Force-HDD). The high-level go-ipmi SetBootDevice sets
// flags MegaRAC firmware (sky SKY-7221) silently ignores — the node then boots
// its disk instead of PXE. This raw form is what those boards honor, and every
// other BMC (iDRAC, iLO, …) accepts it too, so it's used for all hardware.
func (e IPMIExecutor) setBootFlags(ctx context.Context, n Node, dev byte) error {
	client, err := dial(ctx, n)
	if err != nil {
		return err
	}
	defer client.Close(ctx)
	// data: param selector 5, byte1 0xE0 = valid|persistent|EFI, byte2 = device.
	_, err = client.RawCommand(ctx, goipmi.NetFnChassisRequest, 0x08,
		[]byte{0x05, 0xE0, dev, 0x00, 0x00, 0x00}, "set-boot-options")
	return err
}

// SetBootPXE forces the next boot to EFI PXE (the install boot). Set fresh right
// before PowerCycle so the valid bit hasn't expired.
func (e IPMIExecutor) SetBootPXE(ctx context.Context, n Node) error {
	return e.setBootFlags(ctx, n, 0x04)
}

// SetBootDisk forces the boot device to the local disk. Set on restore-done so
// the post-install reboot lands on the installed OS — the install boot arms a
// *persistent* Force-PXE (what MegaRAC needs), so it must be overridden or the
// node re-PXEs and reinstalls forever.
func (e IPMIExecutor) SetBootDisk(ctx context.Context, n Node) error {
	return e.setBootFlags(ctx, n, 0x08)
}

// drivePower brings the chassis to the target power state, RE-ISSUING the
// control command until the chassis actually reports it (bounded). MegaRAC
// firmware silently drops the occasional power on/off, so a single command
// isn't enough — issuing once and moving on leaves a node stuck on (never
// reboots to PXE) or stuck off (never comes back). Already at target = no-op.
func drivePower(ctx context.Context, client *goipmi.Client, on bool) error {
	ctrl := goipmi.ChassisControlPowerDown
	if on {
		ctrl = goipmi.ChassisControlPowerUp
	}
	for attempt := 0; attempt < 6; attempt++ {
		if s, e := client.GetChassisStatus(ctx); e == nil && s.PowerIsOn == on {
			return nil
		}
		if _, err := client.ChassisControl(ctx, ctrl); err != nil {
			return err
		}
		for i := 0; i < 5; i++ { // poll ~10s before re-issuing
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			if s, e := client.GetChassisStatus(ctx); e == nil && s.PowerIsOn == on {
				return nil
			}
		}
	}
	if on {
		return fmt.Errorf("chassis did not power on after retries")
	}
	return fmt.Errorf("chassis did not power off after retries")
}

// PowerCycle cold-cycles the node: force it off (verified), then on (verified).
// A warm ChassisControlPowerCycle is a no-op on MegaRAC, and a single off/on
// can be silently dropped, so we drive each transition to completion — reliable
// on every BMC, never worse.
func (e IPMIExecutor) PowerCycle(ctx context.Context, n Node) error {
	client, err := dial(ctx, n)
	if err != nil {
		return err
	}
	defer client.Close(ctx)
	if err := drivePower(ctx, client, false); err != nil {
		return err
	}
	return drivePower(ctx, client, true)
}

func (e IPMIExecutor) Observe(ctx context.Context, n Node) (Stage, error) {
	if e.Observer == nil {
		return StageNone, nil
	}
	return e.Observer.Stage(ctx, n.MACs)
}
