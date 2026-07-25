package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bigstack-oss/cube-cos-snapshot/internal/model"
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
