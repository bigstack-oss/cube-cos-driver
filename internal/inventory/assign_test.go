package inventory

import "testing"

func TestAssignEnforcesOneMachinePerNode(t *testing.T) {
	s := newStore(t)
	a, _ := s.Create(Input{Label: "a", Address: "1"})
	b, _ := s.Create(Input{Label: "b", Address: "2"})

	if _, err := s.Assign(a.ID, "cl1", "node-1", ""); err != nil {
		t.Fatal(err)
	}
	// Assigning b to the same node must clear a.
	if _, err := s.Assign(b.ID, "cl1", "node-1", ""); err != nil {
		t.Fatal(err)
	}
	ga, _ := s.Get(a.ID)
	gb, _ := s.Get(b.ID)
	if ga.Assignment != nil {
		t.Fatalf("machine a should be unassigned, got %+v", ga.Assignment)
	}
	if gb.Assignment == nil || gb.Assignment.Hostname != "node-1" {
		t.Fatalf("machine b assignment = %+v", gb.Assignment)
	}
}

func TestAssignAllowsMultipleNodesInCluster(t *testing.T) {
	s := newStore(t)
	m, _ := s.Create(Input{Label: "m", Address: "1"})
	s.Assign(m.ID, "cl1", "node-1", "")
	s.Assign(m.ID, "cl1", "node-2", "")
	g, _ := s.Get(m.ID)
	// A server may now be assigned to more than one node in a cluster; the UI
	// surfaces that as a non-blocking duplicate error.
	if len(g.Assignments) != 2 {
		t.Fatalf("expected 2 assignments (within-cluster dup kept), got %+v", g.Assignments)
	}
	// Re-assigning to the same node is an update, not a duplicate.
	s.Assign(m.ID, "cl1", "node-2", "sdb")
	g, _ = s.Get(m.ID)
	if len(g.Assignments) != 2 || g.AssignmentFor("cl1") == nil {
		t.Fatalf("re-assign same node should update, got %+v", g.Assignments)
	}
}

func TestUnassign(t *testing.T) {
	s := newStore(t)
	m, _ := s.Create(Input{Label: "m", Address: "1"})
	s.Assign(m.ID, "cl1", "node-1", "")
	if _, err := s.Unassign(m.ID); err != nil {
		t.Fatal(err)
	}
	g, _ := s.Get(m.ID)
	if g.Assignment != nil {
		t.Fatalf("expected nil assignment, got %+v", g.Assignment)
	}
}

func TestUnassignCluster(t *testing.T) {
	s := newStore(t)
	a, _ := s.Create(Input{Label: "a", Address: "1"})
	b, _ := s.Create(Input{Label: "b", Address: "2"})
	c, _ := s.Create(Input{Label: "c", Address: "3"})
	s.Assign(a.ID, "cl1", "n1", "")
	s.Assign(b.ID, "cl1", "n2", "")
	s.Assign(c.ID, "cl2", "n1", "")

	if err := s.UnassignCluster("cl1"); err != nil {
		t.Fatal(err)
	}
	ga, _ := s.Get(a.ID)
	gb, _ := s.Get(b.ID)
	gc, _ := s.Get(c.ID)
	if ga.Assignment != nil || gb.Assignment != nil {
		t.Fatal("cl1 assignments should be cleared")
	}
	if gc.Assignment == nil {
		t.Fatal("cl2 assignment should remain")
	}
}
