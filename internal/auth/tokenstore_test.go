package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, MachineID())

	if s.Has("gdrive") {
		t.Fatal("Has should be false before Save")
	}
	tok := &oauth2.Token{
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour).Round(time.Second),
	}
	if err := s.Save("gdrive", tok); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !s.Has("gdrive") {
		t.Fatal("Has should be true after Save")
	}

	got, err := s.Load("gdrive")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccessToken != tok.AccessToken || got.RefreshToken != tok.RefreshToken {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, tok)
	}
	if !got.Expiry.Equal(tok.Expiry) {
		t.Fatalf("expiry mismatch: got %v want %v", got.Expiry, tok.Expiry)
	}
}

func TestStoreEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, MachineID())
	tok := &oauth2.Token{AccessToken: "super-secret-token-value"}
	if err := s.Save("conf", tok); err != nil {
		t.Fatalf("Save: %v", err)
	}
	blob, err := os.ReadFile(filepath.Join(dir, "conf.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(blob) == "" {
		t.Fatal("empty token file")
	}
	if strings.Contains(string(blob), "super-secret-token-value") {
		t.Fatal("plaintext token leaked into file on disk")
	}
}

func TestStoreWrongMachineKeyFails(t *testing.T) {
	dir := t.TempDir()
	good := NewStore(dir, MachineID())
	if err := good.Save("gdrive", &oauth2.Token{AccessToken: "x"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A different machine identity must not decrypt the same salt+ciphertext.
	bad := NewStore(dir, []byte("a-different-machine"))
	if _, err := bad.Load("gdrive"); err == nil {
		t.Fatal("Load should fail with the wrong machine key")
	}
}

func TestStoreDeleteMissingIsNoError(t *testing.T) {
	s := NewStore(t.TempDir(), MachineID())
	if err := s.Delete("nope"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}
