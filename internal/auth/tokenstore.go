// Package auth holds shared credential infrastructure for cloud connectors:
// an encrypted-at-rest token store and a browser OAuth authorization flow.
// It is plumbing, not a connector — connectors load and refresh tokens through
// it, but it never touches the Store, indexer, or LLMs.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/scrypt"
	"golang.org/x/oauth2"
)

// Store persists OAuth tokens encrypted at rest under a workspace's auth
// directory. Encryption uses AES-256-GCM with a key derived (scrypt) from a
// machine-stable secret plus a per-workspace random salt. This protects tokens
// copied off the machine; it is not proof against an attacker with read access
// to both the salt file and the same machine identity. No passphrase is
// required, so headless commands (e.g. `grove sync --watch`) work unattended.
type Store struct {
	dir       string
	machineID []byte
}

// NewStore returns a token store rooted at dir (typically Layout.Auth). machineID
// should be a stable per-machine secret; pass MachineID().
func NewStore(dir string, machineID []byte) *Store {
	return &Store{dir: dir, machineID: machineID}
}

type envelope struct {
	Version    int    `json:"v"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, name+".json")
}

// Has reports whether a token is stored for name.
func (s *Store) Has(name string) bool {
	_, err := os.Stat(s.path(name))
	return err == nil
}

// Save encrypts and writes tok under name (0600).
func (s *Store) Save(name string, tok *oauth2.Token) error {
	plain, err := json.Marshal(tok)
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	gcm, err := s.cipher()
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plain, nil)
	blob, err := json.Marshal(envelope{Version: 1, Nonce: nonce, Ciphertext: ct})
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}
	if err := os.WriteFile(s.path(name), blob, 0o600); err != nil {
		return fmt.Errorf("write token %q: %w", name, err)
	}
	return nil
}

// Load decrypts the token stored under name.
func (s *Store) Load(name string) (*oauth2.Token, error) {
	blob, err := os.ReadFile(s.path(name))
	if err != nil {
		return nil, fmt.Errorf("read token %q: %w", name, err)
	}
	var env envelope
	if err := json.Unmarshal(blob, &env); err != nil {
		return nil, fmt.Errorf("parse token envelope %q: %w", name, err)
	}
	gcm, err := s.cipher()
	if err != nil {
		return nil, err
	}
	if len(env.Nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("token %q: bad nonce length", name)
	}
	plain, err := gcm.Open(nil, env.Nonce, env.Ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt token %q (wrong machine key or corrupted): %w", name, err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(plain, &tok); err != nil {
		return nil, fmt.Errorf("unmarshal token %q: %w", name, err)
	}
	return &tok, nil
}

// Delete removes a stored token. A missing token is not an error.
func (s *Store) Delete(name string) error {
	err := os.Remove(s.path(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// cipher derives the AES-GCM cipher from the machine secret and the
// per-workspace salt (created on first use).
func (s *Store) cipher() (cipher.AEAD, error) {
	salt, err := s.salt()
	if err != nil {
		return nil, err
	}
	key, err := scrypt.Key(s.machineID, salt, 1<<15, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// salt reads (or creates) the per-workspace 32-byte salt at <dir>/.salt.
func (s *Store) salt() ([]byte, error) {
	p := filepath.Join(s.dir, ".salt")
	if b, err := os.ReadFile(p); err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("auth salt %q: unexpected length %d", p, len(b))
		}
		return b, nil
	}
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("create auth dir: %w", err)
	}
	if err := os.WriteFile(p, salt, 0o600); err != nil {
		return nil, fmt.Errorf("write salt: %w", err)
	}
	return salt, nil
}

// MachineID returns a stable per-machine, per-user secret used as the scrypt
// password. Hostname + user id are stable across runs and differ between
// machines, so a token file copied to another machine won't decrypt.
func MachineID() []byte {
	host, _ := os.Hostname()
	return fmt.Appendf(nil, "grove-auth\x00%s\x00%d", host, os.Getuid())
}
