// Package secret provides AES-256-GCM encryption for values stored at rest
// (BMC passwords). The key comes from an explicit source or an
// auto-generated key file; a copied data dir is useless without the key.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const keyLen = 32 // AES-256

type Box struct {
	gcm cipher.AEAD
}

// parseKey accepts a 32-byte key as hex (64 chars) or base64.
func parseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) == hex.EncodedLen(keyLen) {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == keyLen {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) == keyLen {
		return b, nil
	}
	return nil, fmt.Errorf("secret: key must be %d bytes (hex or base64)", keyLen)
}

func newBox(key []byte) (*Box, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("secret: key must be %d bytes", keyLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{gcm: gcm}, nil
}

// FromString builds a Box from a hex/base64 key string.
func FromString(key string) (*Box, error) {
	b, err := parseKey(key)
	if err != nil {
		return nil, err
	}
	return newBox(b)
}

// Load resolves a key in priority order:
//  1. keyString (from --secret-key-file contents or SNAPSHOT_SECRET_KEY), if non-empty
//  2. an existing keyFile
//  3. a freshly generated key persisted to keyFile (0600)
func Load(keyString, keyFile string) (*Box, error) {
	if strings.TrimSpace(keyString) != "" {
		return FromString(keyString)
	}
	if data, err := os.ReadFile(keyFile); err == nil {
		return FromString(string(data))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return nil, err
	}
	encoded := hex.EncodeToString(key)
	if err := os.WriteFile(keyFile, []byte(encoded), 0o600); err != nil {
		return nil, err
	}
	return newBox(key)
}

// Encrypt returns base64(nonce||ciphertext).
func (b *Box) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, b.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func (b *Box) Decrypt(encoded string) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	ns := b.gcm.NonceSize()
	if len(sealed) < ns {
		return "", errors.New("secret: ciphertext too short")
	}
	nonce, ct := sealed[:ns], sealed[ns:]
	plaintext, err := b.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
