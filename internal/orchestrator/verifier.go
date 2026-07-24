package orchestrator

import (
	"context"
	"os/exec"
	"time"
)

// Verifier runs the Tier-2 whole-cluster reachability test (control VIP +
// node mgmt IPs) from the server host, once every node is configured.
type Verifier interface {
	Verify(ctx context.Context, targets []string) []PreflightResult
}

// PingVerifier pings each target from the host running the server (the
// pxeserver / control box on the management L2).
type PingVerifier struct{}

func (PingVerifier) Verify(ctx context.Context, targets []string) []PreflightResult {
	var out []PreflightResult
	for _, t := range targets {
		if t == "" {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := exec.CommandContext(c, "ping", "-c", "1", "-W", "2", t).Run()
		cancel()
		out = append(out, PreflightResult{Target: "reach " + t, OK: err == nil})
	}
	return out
}
