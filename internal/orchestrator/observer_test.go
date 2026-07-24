package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLeaseObserverStages(t *testing.T) {
	dir := t.TempDir()
	leases := filepath.Join(dir, "leases")
	access := filepath.Join(dir, "access.log")
	obs := LeaseObserver{LeasesPath: leases, AccessLog: access, MediaSuffix: ".pkg"}

	// No lease yet.
	if s, _ := obs.Stage(context.Background(), []string{"aa:bb:cc:00:00:01"}); s != StageNone {
		t.Fatalf("want none, got %s", s)
	}

	// Lease appears for the MAC → dhcp.
	os.WriteFile(leases, []byte("1700000000 aa:bb:cc:00:00:01 10.0.0.7 node1 *\n"), 0o644)
	if s, _ := obs.Stage(context.Background(), []string{"AA:BB:CC:00:00:01"}); s != StageDHCP {
		t.Fatalf("want dhcp, got %s", s)
	}

	// Access log shows the leased IP fetching media → imaging.
	os.WriteFile(access, []byte(`10.0.0.7 - - "GET /travis_cubecos-x.pkg HTTP/1.1" 200`+"\n"), 0o644)
	if s, _ := obs.Stage(context.Background(), []string{"aa:bb:cc:00:00:01"}); s != StageImaging {
		t.Fatalf("want imaging, got %s", s)
	}

	// Unknown MAC → none.
	if s, _ := obs.Stage(context.Background(), []string{"ff:ff:ff:ff:ff:ff"}); s != StageNone {
		t.Fatalf("want none for unknown mac, got %s", s)
	}
}
