package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	box, err := Load("", filepath.Join(t.TempDir(), ".secret-key"))
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range []string{"", "hunter2", "长密码 with spaces & symbols !@#"} {
		enc, err := box.Encrypt(pt)
		if err != nil {
			t.Fatal(err)
		}
		if enc == pt && pt != "" {
			t.Fatal("ciphertext equals plaintext")
		}
		got, err := box.Decrypt(enc)
		if err != nil {
			t.Fatal(err)
		}
		if got != pt {
			t.Fatalf("roundtrip: got %q want %q", got, pt)
		}
	}
}

func TestNonceIsRandom(t *testing.T) {
	box, _ := Load("", filepath.Join(t.TempDir(), ".secret-key"))
	a, _ := box.Encrypt("same")
	b, _ := box.Encrypt("same")
	if a == b {
		t.Fatal("expected distinct ciphertexts for repeated plaintext")
	}
}

func TestKeyFilePersistedAndReused(t *testing.T) {
	kf := filepath.Join(t.TempDir(), ".secret-key")
	box1, err := Load("", kf)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(kf)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file perms = %v", info.Mode().Perm())
	}
	enc, _ := box1.Encrypt("secret")

	// A second Load reads the same key and can decrypt.
	box2, err := Load("", kf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := box2.Decrypt(enc)
	if err != nil || got != "secret" {
		t.Fatalf("reused key failed: got %q err %v", got, err)
	}
}

func TestWrongKeyFails(t *testing.T) {
	box1, _ := Load("", filepath.Join(t.TempDir(), ".secret-key"))
	box2, _ := Load("", filepath.Join(t.TempDir(), ".secret-key"))
	enc, _ := box1.Encrypt("secret")
	if _, err := box2.Decrypt(enc); err == nil {
		t.Fatal("expected decrypt failure with wrong key")
	}
}

func TestExplicitKeyString(t *testing.T) {
	// 32 bytes hex.
	key := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	box, err := FromString(key)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := box.Encrypt("x")
	if got, _ := box.Decrypt(enc); got != "x" {
		t.Fatal("explicit key roundtrip failed")
	}
	if _, err := FromString("too-short"); err == nil {
		t.Fatal("expected error for short key")
	}
}
