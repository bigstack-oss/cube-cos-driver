// Package agent implements the phone-home agent's logic: check in to the
// snapshot server, sync the clock, run the network preflight, then pull and
// apply the appointed snapshot — reporting progress throughout. All side
// effects (identity, clock, probing, apply, HTTP) are injected so the flow is
// testable without a real node.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type CheckinRequest struct {
	MACs   []string `json:"macs"`
	Serial string   `json:"serial"`
}

type Preflight struct {
	Gateway string   `json:"gateway"`
	DNS     []string `json:"dns"`
	Server  string   `json:"server"`
	Peers   []string `json:"peers"`
}

type CheckinResponse struct {
	Appointed bool `json:"appointed"`
	// Hold: appointed but must wait (master's FTS not done yet); the agent
	// re-checks in until Hold clears.
	Hold          bool      `json:"hold"`
	ClusterID     string    `json:"clusterId"`
	Hostname      string    `json:"hostname"`
	SnapshotURL   string    `json:"snapshotUrl"`
	ServerTimeUTC string    `json:"serverTimeUTC"`
	Preflight     Preflight `json:"preflight"`
}

type PreflightResult struct {
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type ReportRequest struct {
	ClusterID string            `json:"clusterId"`
	Hostname  string            `json:"hostname"`
	State     string            `json:"state"`
	Message   string            `json:"message,omitempty"`
	Preflight []PreflightResult `json:"preflight,omitempty"`
}

// Deps are the injected side effects.
type Deps struct {
	Identity func() (macs []string, serial string)
	SetClock func(t time.Time) error
	// Probe returns true if target (an IP/host) is reachable.
	Probe func(target string) bool
	// Apply downloads snapshotURL and applies it.
	Apply func(ctx context.Context, snapshotURL string) error
	// Configured reports whether this node has finished FTS (the
	// /etc/appliance/state/configured marker exists). Used to (a) self-stop
	// once the cluster is ready and (b) confirm apply completed FTS.
	Configured func() bool
	HTTP       *http.Client
	Now        func() time.Time
	Sleep      func(time.Duration)
}

var ErrPreflightFailed = errors.New("network preflight failed")

// Run performs one full attempt: block (polling) until appointed, then
// time-sync → net-preflight → apply, reporting at each step. Designed to be
// launched by a systemd unit with Restart=on-failure.
func Run(ctx context.Context, server string, d Deps, poll time.Duration) error {
	// Already configured (cluster ready) → nothing to do.
	if d.Configured != nil && d.Configured() {
		return nil
	}
	macs, serial := d.Identity()

	resp, err := d.checkinUntilReady(ctx, server, CheckinRequest{MACs: macs, Serial: serial}, poll)
	if err != nil {
		return err
	}
	if resp.Appointed == false {
		return nil // node became configured while waiting
	}

	// Time sync (before anything TLS/auth-sensitive).
	var results []PreflightResult
	if t, perr := time.Parse(time.RFC3339, resp.ServerTimeUTC); perr == nil {
		skew := d.Now().Sub(t)
		if serr := d.SetClock(t); serr != nil {
			results = append(results, PreflightResult{Target: "time sync", OK: false, Detail: serr.Error()})
		} else {
			results = append(results, PreflightResult{Target: "time sync", OK: true, Detail: fmt.Sprintf("corrected skew %s", skew.Round(time.Second))})
		}
	}

	// Network preflight against the server-provided targets.
	probe := func(label, target string) {
		if target == "" {
			return
		}
		ok := d.Probe(target)
		results = append(results, PreflightResult{Target: fmt.Sprintf("%s %s", label, target), OK: ok})
	}
	probe("gateway", resp.Preflight.Gateway)
	for _, dns := range resp.Preflight.DNS {
		probe("dns", dns)
	}
	probe("server", resp.Preflight.Server)
	for _, peer := range resp.Preflight.Peers {
		probe("peer", peer)
	}

	allOK := true
	for _, r := range results {
		if !r.OK {
			allOK = false
		}
	}
	d.report(ctx, server, ReportRequest{ClusterID: resp.ClusterID, Hostname: resp.Hostname, State: "net-preflight", Preflight: results})
	if !allOK {
		return ErrPreflightFailed
	}

	// Apply the appointed snapshot.
	d.report(ctx, server, ReportRequest{ClusterID: resp.ClusterID, Hostname: resp.Hostname, State: "applying"})
	if err := d.Apply(ctx, resp.SnapshotURL); err != nil {
		d.report(ctx, server, ReportRequest{ClusterID: resp.ClusterID, Hostname: resp.Hostname, State: "error", Message: err.Error()})
		return err
	}
	// "done" means FTS complete — wait for the configured marker so a
	// non-master node only proceeds after this node's FTS truly finishes.
	d.waitConfigured(ctx, poll)
	d.report(ctx, server, ReportRequest{ClusterID: resp.ClusterID, Hostname: resp.Hostname, State: "done", Message: "snapshot applied"})
	return nil
}

// waitConfigured blocks until the node reports configured (FTS complete) or
// the context ends. A nil Configured hook returns immediately.
func (d Deps) waitConfigured(ctx context.Context, poll time.Duration) {
	if d.Configured == nil {
		return
	}
	for !d.Configured() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		d.Sleep(poll)
	}
}

// checkinUntilReady polls check-in until the node is appointed AND cleared to
// apply (not holding for the master). Returns an un-appointed response if the
// node becomes configured while waiting (signal to exit).
func (d Deps) checkinUntilReady(ctx context.Context, server string, req CheckinRequest, poll time.Duration) (CheckinResponse, error) {
	for {
		select {
		case <-ctx.Done():
			return CheckinResponse{}, ctx.Err()
		default:
		}
		if d.Configured != nil && d.Configured() {
			return CheckinResponse{Appointed: false}, nil // cluster ready; stop
		}
		resp, err := d.checkin(ctx, server, req)
		if err == nil && resp.Appointed && !resp.Hold {
			return resp, nil
		}
		d.Sleep(poll)
	}
}

func (d Deps) checkin(ctx context.Context, server string, req CheckinRequest) (CheckinResponse, error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", server+"/api/v1/agents/checkin", bytes.NewReader(body))
	if err != nil {
		return CheckinResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := d.HTTP.Do(httpReq)
	if err != nil {
		return CheckinResponse{}, err
	}
	defer res.Body.Close()
	var out CheckinResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return CheckinResponse{}, err
	}
	return out, nil
}

func (d Deps) report(ctx context.Context, server string, r ReportRequest) {
	body, _ := json.Marshal(r)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", server+"/api/v1/agents/report", bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := d.HTTP.Do(httpReq)
	if err == nil {
		res.Body.Close()
	}
}
