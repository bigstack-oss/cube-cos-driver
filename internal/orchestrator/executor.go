package orchestrator

import (
	"context"
	"sync"
)

// Executor performs the hardware side of a deploy. The real implementation
// talks IPMI + reads pxeserver logs; tests use FakeExecutor.
type Executor interface {
	Preflight(ctx context.Context, n Node) error
	SetBootPXE(ctx context.Context, n Node) error
	PowerCycle(ctx context.Context, n Node) error
	// Observe returns the furthest install stage seen for this node.
	Observe(ctx context.Context, n Node) (Stage, error)
}

// FakeExecutor is a deterministic in-memory executor for tests. Each node
// advances one Observe stage per call (none→dhcp→imaging→done). Hostnames in
// FailPreflight fail their Preflight call.
type FakeExecutor struct {
	mu            sync.Mutex
	observeCalls  map[string]int
	FailPreflight map[string]bool
	StepsToDone   int // Observe calls before reaching done (default 3)
}

func NewFakeExecutor() *FakeExecutor {
	return &FakeExecutor{observeCalls: map[string]int{}, FailPreflight: map[string]bool{}, StepsToDone: 3}
}

func (f *FakeExecutor) Preflight(_ context.Context, n Node) error {
	if f.FailPreflight[n.Hostname] {
		return &PreflightError{Host: n.Hostname}
	}
	return nil
}

func (f *FakeExecutor) SetBootPXE(_ context.Context, _ Node) error { return nil }
func (f *FakeExecutor) PowerCycle(_ context.Context, _ Node) error { return nil }

func (f *FakeExecutor) Observe(_ context.Context, n Node) (Stage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observeCalls[n.Hostname]++
	c := f.observeCalls[n.Hostname]
	steps := f.StepsToDone
	if steps <= 0 {
		steps = 3
	}
	switch {
	case c >= steps:
		return StageDone, nil
	case c >= steps-1:
		return StageImaging, nil
	case c >= 1:
		return StageDHCP, nil
	default:
		return StageNone, nil
	}
}

type PreflightError struct{ Host string }

func (e *PreflightError) Error() string { return "bmc preflight failed for " + e.Host }
