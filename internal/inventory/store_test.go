package inventory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bigstack-oss/cube-cos-driver/internal/secret"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	box, err := secret.Load("", filepath.Join(dir, ".key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(filepath.Join(dir, "machines"), box)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ptr(s string) *string { return &s }

func TestCreateStripsPasswordButFlagsIt(t *testing.T) {
	s := newStore(t)
	m, err := s.Create(Input{Label: "node-1", Address: "10.0.0.1", Username: "admin", Password: ptr("secret")})
	if err != nil {
		t.Fatal(err)
	}
	if m.HasPassword != true {
		t.Fatal("HasPassword should be true")
	}
	// API view never carries the password value or ciphertext (hasPassword
	// is a boolean flag, not the secret).
	b, _ := json.Marshal(m)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "passwordEnc") {
		t.Fatalf("API machine leaks password material: %s", b)
	}
	// On-disk record stores ciphertext, not plaintext.
	raw, _ := os.ReadFile(filepath.Join(s.dir, m.ID+".json"))
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("on-disk record contains plaintext password: %s", raw)
	}
	if !strings.Contains(string(raw), "passwordEnc") {
		t.Fatal("expected encrypted password on disk")
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	s := newStore(t)
	m, _ := s.Create(Input{Label: "n", Address: "10.0.0.2", Username: "root", Password: ptr("p@ss")})
	addr, user, pass, err := s.Credentials(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "10.0.0.2" || user != "root" || pass != "p@ss" {
		t.Fatalf("creds = %q %q %q", addr, user, pass)
	}
}

func TestUpdateKeepsPasswordWhenNil(t *testing.T) {
	s := newStore(t)
	m, _ := s.Create(Input{Label: "n", Address: "10.0.0.3", Username: "u", Password: ptr("keepme")})
	// Update without touching password.
	_, err := s.Update(m.ID, Input{Label: "renamed", Address: "10.0.0.9", Username: "u2", Password: nil})
	if err != nil {
		t.Fatal(err)
	}
	_, _, pass, _ := s.Credentials(m.ID)
	if pass != "keepme" {
		t.Fatalf("password not preserved: %q", pass)
	}
	got, _ := s.Get(m.ID)
	if got.Label != "renamed" || got.BMC.Address != "10.0.0.9" {
		t.Fatalf("update not applied: %+v", got)
	}
	if !got.HasPassword {
		t.Fatal("HasPassword should remain true")
	}
}

func TestUpdateClearsPasswordWhenEmpty(t *testing.T) {
	s := newStore(t)
	m, _ := s.Create(Input{Label: "n", Address: "a", Username: "u", Password: ptr("x")})
	_, err := s.Update(m.ID, Input{Label: "n", Address: "a", Username: "u", Password: ptr("")})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(m.ID)
	if got.HasPassword {
		t.Fatal("password should be cleared")
	}
}

func TestListSortedAndDelete(t *testing.T) {
	s := newStore(t)
	s.Create(Input{Label: "zebra", Address: "a"})
	s.Create(Input{Label: "alpha", Address: "b"})
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Label != "alpha" {
		t.Fatalf("list not sorted: %+v", list)
	}
	if err := s.Delete(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create(Input{Label: "", Address: "a"}); err == nil {
		t.Fatal("expected label required")
	}
	if _, err := s.Create(Input{Label: "n", Address: ""}); err == nil {
		t.Fatal("expected address required")
	}
}

func TestFetchStateAndInventory(t *testing.T) {
	s := newStore(t)
	m, _ := s.Create(Input{Label: "n", Address: "a"})
	if got, _ := s.Get(m.ID); got.FetchState != FetchIdle {
		t.Fatalf("initial state = %q", got.FetchState)
	}
	if err := s.SetFetchState(m.ID, FetchFetching, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(m.ID); got.FetchState != FetchFetching {
		t.Fatal("state not updated")
	}
	if err := s.SetInventory(m.ID, Inventory{Source: "redfish", Serial: "SN1", CPUCount: 2}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(m.ID)
	if got.FetchState != FetchOK || got.Inventory == nil || got.Inventory.Serial != "SN1" {
		t.Fatalf("inventory not stored: %+v", got)
	}
}
