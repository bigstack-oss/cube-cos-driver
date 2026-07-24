package orchestrator

import (
	"context"
	"fmt"

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

func (e IPMIExecutor) SetBootPXE(ctx context.Context, n Node) error {
	client, err := dial(ctx, n)
	if err != nil {
		return err
	}
	defer client.Close(ctx)
	// One-time PXE boot; persist=false so it reverts to disk next boot.
	return client.SetBootDevice(ctx, goipmi.BootDeviceSelectorForcePXE, goipmi.BIOSBootTypeLegacy, false)
}

func (e IPMIExecutor) PowerCycle(ctx context.Context, n Node) error {
	client, err := dial(ctx, n)
	if err != nil {
		return err
	}
	defer client.Close(ctx)
	status, err := client.GetChassisStatus(ctx)
	if err != nil {
		return err
	}
	control := goipmi.ChassisControlPowerCycle
	if !status.PowerIsOn {
		control = goipmi.ChassisControlPowerUp
	}
	_, err = client.ChassisControl(ctx, control)
	return err
}

func (e IPMIExecutor) Observe(ctx context.Context, n Node) (Stage, error) {
	if e.Observer == nil {
		return StageNone, nil
	}
	return e.Observer.Stage(ctx, n.MACs)
}
