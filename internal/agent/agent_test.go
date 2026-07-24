package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeServer records reports and answers checkin with a fixed appointment.
type fakeServer struct {
	mu       sync.Mutex
	reports  []ReportRequest
	appoint  CheckinResponse
	checkins int
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/checkin", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.checkins++
		f.mu.Unlock()
		json.NewEncoder(w).Encode(f.appoint)
	})
	mux.HandleFunc("/api/v1/agents/report", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rr ReportRequest
		json.Unmarshal(body, &rr)
		f.mu.Lock()
		f.reports = append(f.reports, rr)
		f.mu.Unlock()
	})
	return mux
}

func (f *fakeServer) states() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s []string
	for _, r := range f.reports {
		s = append(s, r.State)
	}
	return s
}

func baseDeps(clockSet *time.Time, applied *bool, probe func(string) bool) Deps {
	return Deps{
		Identity: func() ([]string, string) { return []string{"aa:bb:cc:00:00:01"}, "SN1" },
		SetClock: func(t time.Time) error { *clockSet = t; return nil },
		Probe:    probe,
		Apply:    func(_ context.Context, _ string) error { *applied = true; return nil },
		HTTP:     http.DefaultClient,
		Now:      func() time.Time { return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC) },
		Sleep:    func(time.Duration) {},
	}
}

func TestAgentHappyPath(t *testing.T) {
	fs := &fakeServer{appoint: CheckinResponse{
		Appointed:     true,
		Hostname:      "cube-1",
		SnapshotURL:   "http://server/snap",
		ServerTimeUTC: "2026-07-24T12:00:00Z",
		Preflight:     Preflight{Gateway: "10.0.0.254", DNS: []string{"8.8.8.8"}, Server: "10.0.0.1", Peers: []string{"10.0.0.2"}},
	}}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()

	var clockSet time.Time
	var applied bool
	deps := baseDeps(&clockSet, &applied, func(string) bool { return true })

	if err := Run(context.Background(), srv.URL, deps, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if clockSet.IsZero() {
		t.Fatal("clock not synced")
	}
	if !applied {
		t.Fatal("snapshot not applied")
	}
	states := fs.states()
	// Expect net-preflight → applying → done.
	want := []string{"net-preflight", "applying", "done"}
	if len(states) != 3 || states[0] != want[0] || states[1] != want[1] || states[2] != want[2] {
		t.Fatalf("report states = %v, want %v", states, want)
	}
}

func TestAgentHoldsOnPreflightFailure(t *testing.T) {
	fs := &fakeServer{appoint: CheckinResponse{
		Appointed:     true,
		Hostname:      "cube-1",
		SnapshotURL:   "http://server/snap",
		ServerTimeUTC: "2026-07-24T12:00:00Z",
		Preflight:     Preflight{Gateway: "10.0.0.254", Server: "10.0.0.1"},
	}}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()

	var clockSet time.Time
	var applied bool
	// Gateway unreachable.
	deps := baseDeps(&clockSet, &applied, func(target string) bool { return target != "10.0.0.254" })

	err := Run(context.Background(), srv.URL, deps, time.Millisecond)
	if err != ErrPreflightFailed {
		t.Fatalf("want ErrPreflightFailed, got %v", err)
	}
	if applied {
		t.Fatal("must not apply when preflight fails")
	}
	states := fs.states()
	if len(states) != 1 || states[0] != "net-preflight" {
		t.Fatalf("states = %v; expected a single net-preflight report", states)
	}
	// The failing target is recorded.
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var sawFail bool
	for _, r := range fs.reports[0].Preflight {
		if r.Target == "gateway 10.0.0.254" && !r.OK {
			sawFail = true
		}
	}
	if !sawFail {
		t.Fatalf("gateway failure not reported: %+v", fs.reports[0].Preflight)
	}
}

func TestAgentWaitsUntilAppointed(t *testing.T) {
	fs := &fakeServer{}
	// First two checkins: not appointed; third: appointed.
	var mu sync.Mutex
	n := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/checkin", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		appointed := n >= 3
		mu.Unlock()
		json.NewEncoder(w).Encode(CheckinResponse{Appointed: appointed, Hostname: "c1", SnapshotURL: "u", ServerTimeUTC: "2026-07-24T12:00:00Z"})
	})
	mux.HandleFunc("/api/v1/agents/report", func(w http.ResponseWriter, r *http.Request) {})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_ = fs

	var clockSet time.Time
	var applied bool
	deps := baseDeps(&clockSet, &applied, func(string) bool { return true })
	if err := Run(context.Background(), srv.URL, deps, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("should apply after becoming appointed")
	}
}
