package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigstack-oss/cube-cos-driver/internal/model"
)

func TestRunPreflightRetriesThenClears(t *testing.T) {
	var mu sync.Mutex
	reports := 0
	lastPassed := false

	bundle := model.PreflightBundle{
		Hostname: "cube-1",
		Links:    []model.PfLink{{Name: "IF.1", Type: "init", IP: "10.254.0.1/16", Gateway: "10.254.0.254", Roles: []string{"mgmt"}}},
		Peers:    []model.PfPeer{{Hostname: "cube-2", Role: "mgmt", IP: "10.254.0.2"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agents/preflight/checkin":
			json.NewEncoder(w).Encode(PreflightCheckinResponse{
				Appointed:     true,
				ClusterID:     "c1",
				Hostname:      "cube-1",
				ServerTimeUTC: time.Now().UTC().Format(time.RFC3339),
				Bundle:        bundle,
			})
		case "/api/v1/agents/preflight/report":
			var req PreflightReportRequest
			json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			reports++
			lastPassed = req.Passed
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{"message": "ok"})
		case "/api/v1/agents/preflight/greenlight":
			// Clear only once the node has reported a passing round.
			mu.Lock()
			clear := lastPassed
			mu.Unlock()
			json.NewEncoder(w).Encode(GreenlightResponse{Clear: clear})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	var topoCalls, selCalls, probeCalls int
	d := PreflightDeps{
		Identity:          func() ([]string, string) { return []string{"aa:bb:cc:00:00:01"}, "S1" },
		SetClock:          func(time.Time) error { return nil },
		Now:               func() time.Time { return time.Now().Add(500 * time.Millisecond) }, // ~0.5s skew
		Configured:        func() bool { return false },
		ConfigureTopology: func(model.PreflightBundle) (string, error) { topoCalls++; return "map: test", nil },
		Carrier:           func(model.PreflightBundle) (bool, string) { return true, "" },
		// Peer unreachable on the first probe, reachable after; gateway always up.
		Probe: func(target string) bool {
			probeCalls++
			if target == "10.254.0.2" && probeCalls <= 1 {
				return false
			}
			return true
		},
		WriteSEL: func(_, _, _ string) error { selCalls++; return nil },
		HTTP:     srv.Client(),
		Sleep:    func(time.Duration) {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := RunPreflight(ctx, srv.URL, d, time.Millisecond); err != nil {
		t.Fatalf("RunPreflight: %v", err)
	}

	if topoCalls != 1 {
		t.Errorf("ConfigureTopology called %d times, want 1", topoCalls)
	}
	mu.Lock()
	defer mu.Unlock()
	if reports < 2 {
		t.Errorf("expected at least 2 report rounds (retry), got %d", reports)
	}
	if !lastPassed {
		t.Error("final report should be passed=true")
	}
	if selCalls == 0 {
		t.Error("expected SEL phase mirroring")
	}
}

func TestRunPreflightSelfStopsWhenConfigured(t *testing.T) {
	d := PreflightDeps{
		Configured: func() bool { return true },
		Identity:   func() ([]string, string) { return nil, "" },
		Sleep:      func(time.Duration) {},
	}
	if err := RunPreflight(context.Background(), "http://unused", d, time.Millisecond); err != nil {
		t.Fatalf("configured node should no-op, got %v", err)
	}
}

// Rich report: per-member carrier rows, clock-skew row, and local persistence
// every round (so the node CLI can show the report when the driver is
// unreachable).
func TestRunPreflightRichReportAndLocalSave(t *testing.T) {
	var mu sync.Mutex
	var lastReq PreflightReportRequest
	saved := 0

	bundle := model.PreflightBundle{
		Hostname: "cube-1",
		Links:    []model.PfLink{{Name: "bond0", Type: "bond", Members: []string{"IF.1", "IF.2"}, IP: "10.254.0.1/16", Gateway: "10.254.0.254", Roles: []string{"mgmt"}}},
		Peers:    []model.PfPeer{{Hostname: "cube-2", Role: "mgmt", IP: "10.254.0.2"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agents/preflight/checkin":
			json.NewEncoder(w).Encode(PreflightCheckinResponse{
				Appointed: true, ClusterID: "c1", Hostname: "cube-1",
				ServerTimeUTC: time.Now().UTC().Format(time.RFC3339), Bundle: bundle,
			})
		case "/api/v1/agents/preflight/report":
			var req PreflightReportRequest
			json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			lastReq = req
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{"message": "ok"})
		case "/api/v1/agents/preflight/greenlight":
			json.NewEncoder(w).Encode(GreenlightResponse{Clear: true})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	d := PreflightDeps{
		Identity:          func() ([]string, string) { return []string{"aa:bb:cc:00:00:01"}, "S1" },
		SetClock:          func(time.Time) error { return nil },
		Now:               func() time.Time { return time.Now() },
		Configured:        func() bool { return false },
		ConfigureTopology: func(model.PreflightBundle) (string, error) { return "map: test", nil },
		CarrierChecks: func(model.PreflightBundle) []PreflightResult {
			return []PreflightResult{
				{Target: "bond0 member IF.1 (eth0) link", OK: true, Detail: "10000Mb/s"},
				{Target: "bond0 member IF.2 (eth1) link", OK: true, Detail: "10000Mb/s"},
			}
		},
		Probe:      func(string) bool { return true },
		SaveReport: func(PreflightReportRequest) { mu.Lock(); saved++; mu.Unlock() },
		HTTP:       srv.Client(),
		Sleep:      func(time.Duration) {},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := RunPreflight(ctx, srv.URL, d, time.Millisecond); err != nil {
		t.Fatalf("RunPreflight: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if saved == 0 {
		t.Error("SaveReport was never called")
	}
	has := func(sub string) bool {
		for _, m := range lastReq.Matrix {
			if strings.Contains(m.Target, sub) {
				return true
			}
		}
		return false
	}
	if !has("bond0 member IF.1") || !has("bond0 member IF.2") {
		t.Errorf("matrix missing per-member carrier rows: %+v", lastReq.Matrix)
	}
	if !has("clock-skew") {
		t.Errorf("matrix missing clock-skew row: %+v", lastReq.Matrix)
	}
	if !lastReq.CarrierOK || !lastReq.Passed {
		t.Errorf("expected carrierOk+passed from all-ok checks: %+v", lastReq)
	}
}

// An operator rekick (seq bump in the report response) makes the parked agent
// re-checkin — fresh bundle + snapshot — without any reboot.
func TestRunPreflightRekicksInPlace(t *testing.T) {
	var mu sync.Mutex
	checkins, reports := 0, 0
	var seq int64

	bundle := model.PreflightBundle{
		Hostname: "cube-1",
		Links:    []model.PfLink{{Name: "IF.1", Type: "init", IP: "10.254.0.1/16", Gateway: "10.254.0.254", Roles: []string{"mgmt"}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agents/preflight/checkin":
			mu.Lock()
			checkins++
			mu.Unlock()
			json.NewEncoder(w).Encode(PreflightCheckinResponse{
				Appointed: true, ClusterID: "c1", Hostname: "cube-1",
				ServerTimeUTC: time.Now().UTC().Format(time.RFC3339), Bundle: bundle,
			})
		case "/api/v1/agents/preflight/report":
			mu.Lock()
			reports++
			// Bump the seq after the 2nd report — an operator rekick.
			if reports == 2 {
				seq = 1
			}
			out := PreflightReportResponse{Message: "ok", RekickSeq: seq}
			mu.Unlock()
			json.NewEncoder(w).Encode(out)
		case "/api/v1/agents/preflight/greenlight":
			mu.Lock()
			// Clear only after the rekicked (2nd) checkin has reported again.
			clear := checkins >= 2 && reports >= 3
			mu.Unlock()
			json.NewEncoder(w).Encode(GreenlightResponse{Clear: clear})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	topoCalls := 0
	d := PreflightDeps{
		Identity:          func() ([]string, string) { return []string{"aa:bb:cc:00:00:01"}, "S1" },
		SetClock:          func(time.Time) error { return nil },
		Now:               func() time.Time { return time.Now() },
		Configured:        func() bool { return false },
		ConfigureTopology: func(model.PreflightBundle) (string, error) { topoCalls++; return "map: test", nil },
		Carrier:           func(model.PreflightBundle) (bool, string) { return true, "" },
		Probe:             func(string) bool { return true },
		HTTP:              srv.Client(),
		Sleep:             func(time.Duration) {},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := RunPreflight(ctx, srv.URL, d, time.Millisecond); err != nil {
		t.Fatalf("RunPreflight: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if checkins < 2 {
		t.Errorf("rekick should force a fresh checkin, got %d", checkins)
	}
	if topoCalls < 2 {
		t.Errorf("rekick should reconfigure topology, got %d", topoCalls)
	}
}

// The agent sets the node clock from the driver, so the offset it just
// corrected must not be what it reports: the driver gates green light 1 on the
// reported skew, and a node whose RTC was seconds out would otherwise be held
// forever on a skew that no longer exists.
func TestSyncClockReportsResidualNotCorrectedOffset(t *testing.T) {
	server := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	nodeClock := server.Add(13750 * time.Millisecond) // RTC 13.75s fast

	d := PreflightDeps{
		Now: func() time.Time { return nodeClock },
		SetClock: func(to time.Time) error {
			nodeClock = to // the agent corrects it
			return nil
		},
	}

	residual, corrected := d.syncClock(server.Format(time.RFC3339))

	if corrected < 13.7 || corrected > 13.8 {
		t.Errorf("corrected offset: want ~13.75s for diagnostics, got %.2fs", corrected)
	}
	if residual > 0.5 || residual < -0.5 {
		t.Errorf("residual skew after correction: want ~0s (the gate input), got %.2fs", residual)
	}
}

// A correction that fails leaves the node genuinely out of sync, so the offset
// must still be reported and still gate.
func TestSyncClockKeepsSkewWhenCorrectionFails(t *testing.T) {
	server := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	for name, dep := range map[string]PreflightDeps{
		"SetClock errors": {
			Now:      func() time.Time { return server.Add(9 * time.Second) },
			SetClock: func(time.Time) error { return context.DeadlineExceeded },
		},
		"no SetClock wired": {
			Now: func() time.Time { return server.Add(9 * time.Second) },
		},
	} {
		residual, _ := dep.syncClock(server.Format(time.RFC3339))
		if residual < 8.5 || residual > 9.5 {
			t.Errorf("%s: uncorrected skew must still be reported, got %.2fs", name, residual)
		}
	}
}
