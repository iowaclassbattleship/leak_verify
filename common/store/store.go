// Package store keeps the little that has to outlive a process: each user's
// marking key and their issuance log.
//
// Marked copies are not stored. Marking is deterministic in the key, the
// source, the mark ID and the techniques, so a copy can always be regenerated
// from those four. The key is never written to disk either: it is derived from
// a server secret and the user's address, so it comes back identical after a
// restart without anything sensitive sitting in a file.
package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// KeyFor derives a user's marking key. Change the secret and every previously
// issued copy stops decoding, so treat it as long-lived.
func KeyFor(secret, email string) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("marking-key\x00"))
	m.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	return m.Sum(nil)
}

var unsafe = regexp.MustCompile(`[^a-z0-9._@-]+`)

// Store is a directory of per-user logs. A zero Dir disables persistence, so
// the applications still run with nothing on disk.
type Store struct {
	Dir string
	mu  sync.Mutex
}

func (s *Store) path(email, name string) string {
	who := unsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(email)), "_")
	return filepath.Join(s.Dir, who, name+".json")
}

// Load reads one log into v. A missing file is not an error: it just means
// this user has issued nothing yet.
func (s *Store) Load(email, name string, v any) error {
	if s == nil || s.Dir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path(email, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// Save writes one log, replacing it atomically so a crash mid-write cannot
// leave a half-written file behind.
func (s *Store) Save(email, name string, v any) error {
	if s == nil || s.Dir == "" {
		return nil
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path(email, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
