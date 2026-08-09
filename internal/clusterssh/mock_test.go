package clusterssh

import (
	"context"
	"strings"
	"testing"
)

func TestMockRecordsAndScripts(t *testing.T) {
	var got []string
	m := &MockClient{Script: func(cmd string) ([]string, error) {
		if strings.Contains(cmd, "framework_list") {
			return []string{"proj-a", "proj-b"}, nil
		}
		return nil, nil
	}}
	if err := m.Run(context.Background(), "hex_cli -c app -c framework_list", func(l string) { got = append(got, l) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "proj-a" {
		t.Fatalf("lines=%v", got)
	}
	_ = m.Push(context.Background(), "/a", "/b")
	if len(m.Runs) != 1 || len(m.Pushes) != 1 {
		t.Fatalf("recorded runs=%v pushes=%v", m.Runs, m.Pushes)
	}
}
