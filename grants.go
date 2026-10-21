package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// grant is what one person has allowed mcp-fuse to do at one backend: the tokens that backend's authorization
// server issued to mcp-fuse for that person.
type grant struct {
	Access        string    `json:"a"`
	Refresh       string    `json:"r,omitempty"`
	Expiry        time.Time `json:"e"`
	ClientID      string    `json:"c"`
	TokenEndpoint string    `json:"t"`
	Resource      string    `json:"res"`
}

type stateData struct {
	Grants  map[string]map[string]*grant `json:"grants"`  // person -> backend -> grant
	Clients map[string]string            `json:"clients"` // backend|issuer -> client_id from dynamic registration
}

// store keeps the grants in one file, encrypted. The key comes from the secret file; without it the file is
// noise. Losing the file loses the grants, and every person connects again - nothing else depends on it.
type store struct {
	path string
	aead cipher.AEAD
	mu   sync.Mutex
	d    stateData
}

const storeMagic = "MFS1"

const minSecretBytes = 32

// readSecret returns the secret file's content without surrounding whitespace.
func readSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	secret := bytes.TrimSpace(data)
	if len(secret) < minSecretBytes {
		return nil, fmt.Errorf("%s must hold at least %d bytes", path, minSecretBytes)
	}
	return secret, nil
}

// deriveKey gives each use of the secret its own key, so that a value signed for one purpose is not valid for
// another.
func deriveKey(secret []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("mcp-fuse " + purpose + " v1"))
	return mac.Sum(nil)
}

func openStore(path string, secret []byte) (*store, error) {
	block, err := aes.NewCipher(deriveKey(secret, "state"))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &store{path: path, aead: aead, d: stateData{Grants: map[string]map[string]*grant{}, Clients: map[string]string{}}}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	if len(data) < len(storeMagic)+aead.NonceSize() || string(data[:len(storeMagic)]) != storeMagic {
		return nil, fmt.Errorf("%s is not a state file", path)
	}
	rest := data[len(storeMagic):]
	plain, err := aead.Open(nil, rest[:aead.NonceSize()], rest[aead.NonceSize():], []byte(storeMagic))
	if err != nil {
		return nil, fmt.Errorf("%s cannot be decrypted with this secret", path)
	}
	if err := json.Unmarshal(plain, &s.d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.d.Grants == nil {
		s.d.Grants = map[string]map[string]*grant{}
	}
	if s.d.Clients == nil {
		s.d.Clients = map[string]string{}
	}
	return s, nil
}

// saveLocked writes the file through a temporary one so that a crash leaves the old state, never half of a new
// one. The caller holds s.mu.
func (s *store) saveLocked() error {
	plain, err := json.Marshal(s.d)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out := append([]byte(storeMagic), nonce...)
	out = s.aead.Seal(out, nonce, plain, []byte(storeMagic))

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".mcp-fuse-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func (s *store) grant(person, backend string) (grant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.d.Grants[person][backend]
	if g == nil {
		return grant{}, false
	}
	return *g, true
}

func (s *store) putGrant(person, backend string, g grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Grants[person] == nil {
		s.d.Grants[person] = map[string]*grant{}
	}
	s.d.Grants[person][backend] = &g
	return s.saveLocked()
}

func (s *store) deleteGrant(person, backend string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Grants[person][backend] == nil {
		return nil
	}
	delete(s.d.Grants[person], backend)
	if len(s.d.Grants[person]) == 0 {
		delete(s.d.Grants, person)
	}
	return s.saveLocked()
}

func (s *store) client(backend, issuer string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.d.Clients[backend+"|"+issuer]
	return id, ok
}

func (s *store) putClient(backend, issuer, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Clients[backend+"|"+issuer] = clientID
	return s.saveLocked()
}

// A connect link carries a ticket instead of a login: the person opening it in a browser has no token for
// mcp-fuse, so the ticket - signed, short-lived, naming one person and one backend - is what says who is
// connecting what.
type ticket struct {
	Person  string `json:"u"`
	Backend string `json:"b"`
	Expires int64  `json:"x"`
}

const ticketTTL = 10 * time.Minute

func signTicket(secret []byte, t ticket) string {
	payload, _ := json.Marshal(t)
	body := b64(payload)
	mac := hmac.New(sha256.New, deriveKey(secret, "ticket"))
	mac.Write([]byte(body))
	return body + "." + b64(mac.Sum(nil))
}

func verifyTicket(secret []byte, token string, now time.Time) (ticket, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return ticket{}, errors.New("malformed ticket")
	}
	mac := hmac.New(sha256.New, deriveKey(secret, "ticket"))
	mac.Write([]byte(body))
	want := b64(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return ticket{}, errors.New("bad ticket signature")
	}
	payload, err := unb64(body)
	if err != nil {
		return ticket{}, err
	}
	var t ticket
	if err := json.Unmarshal(payload, &t); err != nil {
		return ticket{}, err
	}
	if now.Unix() > t.Expires {
		return ticket{}, errors.New("ticket expired")
	}
	return t, nil
}
