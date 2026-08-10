package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/model"
)

// PreflightDeps are the injected side effects of the installer-phase (pre-restore)
// preflight. Real implementations live in cmd/phone-home-agent; tests inject
// fakes so the flow needs no real NICs, BMC, or network.
type PreflightDeps struct {
	Identity func() (macs []string, serial string)
	SetClock func(t time.Time) error
	Now      func() time.Time
	// Configured reports FTS-complete; a re-run after the cluster is up is a no-op.
	Configured func() bool
	// ConfigureTopology stands up the node's bonds/VLANs/role IPs transiently
	// and returns a diagnostic string (the resolved IF→device map + per-device
	// carrier) so the server can see the actual interface binding.
	ConfigureTopology func(b model.PreflightBundle) (diag string, err error)
	// FetchSnapshot downloads the appointed snapshot to a RAM path, on the
	// stable initial network before topology reconfig. hex_autoinstall persists
	// it to /store after restore for a local (network-free) OS-phase apply.
	FetchSnapshot func(url string) error
	// StampEnv records the node's OS-phase identity (hostname, cluster, master
	// role, and the operator-picked OS disk) to RAM so the installer can persist
	// it into the restored OS and target the restore at the chosen disk.
	StampEnv func(host, cluster string, isMaster bool, osDisk string, optOutRepair, simulateAirgap bool)
	// Carrier reports whether every intended bond member has carrier (link up);
	// detail names the offending member when not.
	Carrier func(b model.PreflightBundle) (ok bool, detail string)
	// CarrierChecks returns one row per link the topology depends on (bond
	// members, role links) so the report names exactly which port is down.
	// Preferred over Carrier when set.
	CarrierChecks func(b model.PreflightBundle) []PreflightResult
	// SaveReport persists the round's full report locally (best effort) so an
	// operator can read it on the node (--report) when the driver is
	// unreachable.
	SaveReport func(r PreflightReportRequest)
	// Probe pings a target IP, returning reachability.
	Probe func(target string) bool
	// WriteSEL mirrors a phase transition to the BMC out-of-band (best effort;
	// may be nil).
	WriteSEL func(phase, result, detail string) error
	// WaitGate reports whether the driver's restore-go record is present on this
	// node's BMC (OOB, read via local KCS). When set it is the AUTHORITATIVE
	// green light 1 — the driver may be unreachable in-band after the topology
	// reconfig, so GL1 rides the BMC. Nil falls back to the network greenlight.
	WaitGate func() bool
	// FetchSetReady best-effort pre-fetches the operator's set_ready params from
	// the driver during preflight (network up) and stages them locally, so the
	// master can run set_ready offline post-apply (mgmt off the flat L2 by then).
	// Skips silently if params aren't submitted yet; never fails preflight. Nil = off.
	FetchSetReady func(clusterID string) error
	HTTP          *http.Client
	Sleep         func(time.Duration)
}

// RunPreflight performs the installer-phase validation: check in, sync the
// clock, stand up the full topology, then loop (carrier + peer/gateway ping
// matrix) reporting each round until the node passes and green light 1 clears.
// Returns nil when cleared to restore (or when the node is already configured).
func RunPreflight(ctx context.Context, server string, d PreflightDeps, poll time.Duration) error {
	if d.Configured != nil && d.Configured() {
		return nil
	}
	macs, serial := d.Identity()
	req := CheckinRequest{MACs: macs, Serial: serial}

	// baseline is the rekick seq adopted at the first successful report; a
	// later bump means the operator requested an in-place re-run.
	baseline := int64(-1)

recheck:
	for {
		resp, err := d.preflightCheckinUntilAppointed(ctx, server, req, poll)
		if err != nil {
			return err
		}
		if resp.Inspect {
			return ErrInspect // caller reports hardware inventory + halts
		}
		if !resp.Appointed {
			return nil
		}

		// Time sync — report the skew so the server can enforce the ±5s fleet gate.
		skew := d.syncClock(resp.ServerTimeUTC)

		// Pre-fetch the snapshot NOW, while the initial network is still up. The
		// next step (ConfigureTopology) may sever the SPA link, and /store does not
		// exist until restore, so we hold it in RAM and persist it post-restore.
		prefetch := PreflightResult{Target: "snapshot-prefetch", OK: true, Detail: "not attempted"}
		if d.FetchSnapshot != nil && resp.SnapshotURL != "" {
			if e := d.FetchSnapshot(resp.SnapshotURL); e != nil {
				log.Printf("preflight: snapshot pre-fetch failed: %v (OS phase will fall back to download)", e)
				prefetch = PreflightResult{Target: "snapshot-prefetch", OK: false, Detail: e.Error() + " (OS phase will retry the download)"}
			} else {
				log.Printf("preflight: snapshot pre-fetched to RAM for post-restore local apply")
				prefetch = PreflightResult{Target: "snapshot-prefetch", OK: true, Detail: "staged to RAM"}
			}
		}
		if d.StampEnv != nil {
			d.StampEnv(resp.Hostname, resp.ClusterID, resp.IsMaster, resp.OSDisk, resp.OptOutRepair, resp.SimulateAirgap)
			log.Printf("preflight: stamped OS-phase identity host=%s master=%v osDisk=%s optOutRepair=%v simulateAirgap=%v", resp.Hostname, resp.IsMaster, resp.OSDisk, resp.OptOutRepair, resp.SimulateAirgap)
		}
		// Pre-fetch the operator's set_ready params now (network up), like the
		// snapshot, so the master can finalize the cluster offline. Best-effort.
		if d.FetchSetReady != nil {
			if e := d.FetchSetReady(resp.ClusterID); e != nil {
				log.Printf("preflight: set_ready params pre-fetch: %v (best effort)", e)
			}
		}

		// SEL self-test BEFORE the network reconfig. ConfigureTopology can sever the
		// bootstrap (DHCP) route the agent used to reach the SPA, so while in-band is
		// still guaranteed we probe the OOB channel (write a marker to /dev/ipmi0)
		// and report whether it works — the server then knows if it can fall back to
		// reading SEL for this node's post-topology status.
		selErr := ""
		if d.WriteSEL != nil {
			if e := d.WriteSEL("preflight", "started", ""); e != nil {
				selErr = e.Error()
			}
		}
		started := PreflightReportRequest{
			ClusterID: resp.ClusterID, Hostname: resp.Hostname, ClockSkewSec: skew, Passed: false,
			Matrix: []PreflightResult{
				{Target: "preflight-started", OK: true},
				{Target: "sel-capable", OK: selErr == "", Detail: selErr},
			},
		}
		d.saveReport(started)
		if seq, ok := d.preflightReport(ctx, server, started); ok && baseline == -1 {
			baseline = seq
		}

		// Stand up the transient topology (best-effort): a failure is reported via
		// the matrix each round, not fatal — the ping test still validates whatever
		// connectivity exists. Re-runs teardown-first (cmd side) so a rekicked
		// bundle applies cleanly.
		log.Printf("preflight: appointed host=%s cluster=%s skew=%.4fs", resp.Hostname, resp.ClusterID, skew)
		var topoErr, topoDiag string
		if d.ConfigureTopology != nil {
			diag, terr := d.ConfigureTopology(resp.Bundle)
			topoDiag = diag
			log.Printf("preflight: topology configured: %s", topoDiag)
			if terr != nil {
				topoErr = terr.Error()
				log.Printf("preflight: topology ERROR: %s", topoErr)
				d.sel("preflight", "topology-error", topoErr)
			}
		}
		targets := pingTargets(resp.Bundle)

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if d.Configured != nil && d.Configured() {
				return nil
			}

			carrierOK, cdetail := true, ""
			var carrierRows []PreflightResult
			if d.CarrierChecks != nil {
				carrierRows = d.CarrierChecks(resp.Bundle)
				for _, r := range carrierRows {
					if !r.OK {
						carrierOK = false
						if cdetail == "" {
							cdetail = r.Target + ": " + r.Detail
						}
					}
				}
			} else if d.Carrier != nil {
				carrierOK, cdetail = d.Carrier(resp.Bundle)
			}
			var matrix []PreflightResult
			if topoDiag != "" || topoErr != "" {
				dd := topoDiag
				if topoErr != "" {
					dd = topoErr + " | " + topoDiag
				}
				matrix = append(matrix, PreflightResult{Target: "topology", OK: topoErr == "", Detail: dd})
			}
			matrix = append(matrix, carrierRows...)
			allReach := true
			for _, t := range targets {
				ok := d.Probe(t.ip)
				if !ok {
					allReach = false
				}
				matrix = append(matrix, PreflightResult{Target: t.label, OK: ok, Detail: map[bool]string{true: "reachable", false: "unreachable"}[ok]})
			}
			// Informational rows (do not gate passed; the server gates fleet skew).
			matrix = append(matrix,
				PreflightResult{Target: "clock-skew", OK: skew < 5 && skew > -5, Detail: fmt.Sprintf("%+.2fs vs driver", skew)},
				prefetch,
			)
			passed := carrierOK && allReach && topoErr == ""
			if passed {
				log.Printf("preflight: round PASSED — carrier ok, all peers+gateway reachable; requesting green light 1")
			} else {
				var f []string
				for _, m := range matrix {
					if !m.OK {
						f = append(f, m.Target)
					}
				}
				log.Printf("preflight: round not-passed carrier=%v topoErr=%q fails=%v", carrierOK, topoErr, f)
			}

			round := PreflightReportRequest{
				ClusterID:    resp.ClusterID,
				Hostname:     resp.Hostname,
				CarrierOK:    carrierOK,
				ClockSkewSec: skew,
				Matrix:       matrix,
				Passed:       passed,
			}
			d.saveReport(round)
			if seq, ok := d.preflightReport(ctx, server, round); ok {
				if baseline == -1 {
					baseline = seq
				} else if seq > baseline {
					baseline = seq
					log.Printf("preflight: operator requested an in-place re-run (seq %d) — re-checking in for the updated config", seq)
					d.sel("preflight", "rekick", "")
					continue recheck
				}
			}
			if passed {
				d.sel("preflight", "ok", "")
				// Best-effort HTTP greenlight — for faster UI + telemetry when the
				// driver is still reachable. It never gates.
				_, _ = d.greenlight(ctx, server, resp.ClusterID, resp.Hostname)
				// Authoritative GL1: the driver's restore-go SEL on our BMC (OOB).
				if d.WaitGate != nil {
					if d.WaitGate() {
						return nil // cleared to restore (SEL go)
					}
				} else if clear, gerr := d.greenlight(ctx, server, resp.ClusterID, resp.Hostname); gerr == nil && clear {
					return nil // no OOB gate wired (tests): fall back to network greenlight
				}
			} else if !carrierOK {
				d.sel("preflight", "carrier-fail", cdetail)
			}
			d.Sleep(poll)
		}
	}
}

func (d PreflightDeps) syncClock(serverTimeUTC string) float64 {
	t, err := time.Parse(time.RFC3339, serverTimeUTC)
	if err != nil {
		return 0
	}
	skew := d.Now().Sub(t).Seconds()
	if d.SetClock != nil {
		_ = d.SetClock(t)
	}
	return skew
}

type pingTarget struct {
	label string
	ip    string
}

// pingTargets is the peer + gateway ping matrix for a bundle.
func pingTargets(b model.PreflightBundle) []pingTarget {
	var out []pingTarget
	for _, p := range b.Peers {
		out = append(out, pingTarget{label: fmt.Sprintf("peer %s %s %s", p.Hostname, p.Role, p.IP), ip: p.IP})
	}
	for _, l := range b.Links {
		if l.Gateway != "" {
			out = append(out, pingTarget{label: "gateway " + l.Gateway, ip: l.Gateway})
		}
	}
	return out
}

func (d PreflightDeps) saveReport(r PreflightReportRequest) {
	if d.SaveReport != nil {
		d.SaveReport(r)
	}
}

func (d PreflightDeps) sel(phase, result, detail string) {
	if d.WriteSEL != nil {
		_ = d.WriteSEL(phase, result, detail)
	}
}

func (d PreflightDeps) preflightCheckinUntilAppointed(ctx context.Context, server string, req CheckinRequest, poll time.Duration) (PreflightCheckinResponse, error) {
	for {
		select {
		case <-ctx.Done():
			return PreflightCheckinResponse{}, ctx.Err()
		default:
		}
		if d.Configured != nil && d.Configured() {
			return PreflightCheckinResponse{Appointed: false}, nil
		}
		resp, err := d.postJSON(ctx, server+"/api/v1/agents/preflight/checkin", req)
		if err == nil {
			var out PreflightCheckinResponse
			if json.Unmarshal(resp, &out) == nil && (out.Appointed || out.Inspect) {
				return out, nil
			}
		}
		d.Sleep(poll)
	}
}

// preflightReport sends a round's report; returns the server's rekick seq for
// this node (ok=false when the send failed — the seq is then meaningless).
func (d PreflightDeps) preflightReport(ctx context.Context, server string, r PreflightReportRequest) (int64, bool) {
	body, err := d.postJSON(ctx, server+"/api/v1/agents/preflight/report", r)
	if err != nil {
		return 0, false
	}
	var resp PreflightReportResponse
	if json.Unmarshal(body, &resp) != nil {
		return 0, false
	}
	return resp.RekickSeq, true
}

func (d PreflightDeps) greenlight(ctx context.Context, server, clusterID, hostname string) (bool, error) {
	body, err := d.postJSON(ctx, server+"/api/v1/agents/preflight/greenlight",
		map[string]string{"clusterId": clusterID, "hostname": hostname})
	if err != nil {
		return false, err
	}
	var out GreenlightResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return false, err
	}
	return out.Clear, nil
}

func (d PreflightDeps) postJSON(ctx context.Context, url string, payload any) ([]byte, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := d.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(res.Body)
	return buf.Bytes(), nil
}
