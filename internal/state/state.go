// Package state keeps what mcp-fuse must not lose: the grants people have given, encrypted in one file. It also
// holds the signed tickets that carry a person to a connect page.
package state

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// Grant is what one person has allowed mcp-fuse to do at one backend: the tokens that backend's authorization
// server issued to mcp-fuse for that person.
type Grant struct {
	Access        string    `json:"a"`
	Refresh       string    `json:"r,omitempty"`
	Expiry        time.Time `json:"e"`
	ClientID      string    `json:"c"`
	TokenEndpoint string    `json:"t"`
	Resource      string    `json:"res"`
}

type stateData struct {
	Grants  map[string]map[string]*Grant `json:"grants"`  // person -> backend -> grant
	Clients map[string]string            `json:"clients"` // backend|issuer -> client_id from dynamic registration
}

// store keeps the grants in one file, encrypted. The key comes from the secret file; without it the file is
// noise. Losing the file loses the grants, and every person connects again - nothing else depends on it.
type Store struct {
	path string
	aead cipher.AEAD
	mu   sync.Mutex
	d    stateData
}

const storeMagic = "MFS1"

const minSecretBytes = 32

// ReadSecret returns the secret file's content without surrounding whitespace.
func ReadSecret(path string) ([]byte, error) {
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

// Open reads the state file, or starts an empty state if there is none.
func Open(path string, secret []byte) (*Store, error) {
	block, err := aes.NewCipher(deriveKey(secret, "state"))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, aead: aead, d: stateData{Grants: map[string]map[string]*Grant{}, Clients: map[string]string{}}}

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
		s.d.Grants = map[string]map[string]*Grant{}
	}
	if s.d.Clients == nil {
		s.d.Clients = map[string]string{}
	}
	return s, nil
}

// saveLocked writes the file through a temporary one so that a crash leaves the old state, never half of a new
// one. The caller holds s.mu.
func (s *Store) saveLocked() error {
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

// Get returns the grant a person has given for a backend.
func (s *Store) Get(person, backend string) (Grant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.d.Grants[person][backend]
	if g == nil {
		return Grant{}, false
	}
	return *g, true
}

// Put stores or replaces a grant.
func (s *Store) Put(person, backend string, g Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Grants[person] == nil {
		s.d.Grants[person] = map[string]*Grant{}
	}
	s.d.Grants[person][backend] = &g
	return s.saveLocked()
}

// Delete drops a grant; the person has to connect again.
func (s *Store) Delete(person, backend string) error {
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

// Client returns the client id registered for a backend at an issuer.
func (s *Store) Client(backend, issuer string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.d.Clients[backend+"|"+issuer]
	return id, ok
}

// PutClient remembers a client id from dynamic registration.
func (s *Store) PutClient(backend, issuer, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Clients[backend+"|"+issuer] = clientID
	return s.saveLocked()
}

// A connect link carries a ticket instead of a login: the person opening it in a browser has no token for
// mcp-fuse, so the ticket - signed, short-lived, naming one person and one backend - is what says who is
// connecting what.
// Ticket names one person and one backend. It says whom a link was made for and proves nothing about who opens it.
type Ticket struct {
	Person  string `json:"u"`
	Backend string `json:"b"`
	Expires int64  `json:"x"`
}

// TicketTTL is how long a connect link stays valid.
const TicketTTL = 10 * time.Minute

func SignTicket(secret []byte, t Ticket) string {
	payload, _ := json.Marshal(t)
	body := b64(payload)
	mac := hmac.New(sha256.New, deriveKey(secret, "ticket"))
	mac.Write([]byte(body))
	return body + "." + b64(mac.Sum(nil))
}

func VerifyTicket(secret []byte, token string, now time.Time) (Ticket, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return Ticket{}, errors.New("malformed ticket")
	}
	mac := hmac.New(sha256.New, deriveKey(secret, "ticket"))
	mac.Write([]byte(body))
	want := b64(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return Ticket{}, errors.New("bad ticket signature")
	}
	payload, err := unb64(body)
	if err != nil {
		return Ticket{}, err
	}
	var t Ticket
	if err := json.Unmarshal(payload, &t); err != nil {
		return Ticket{}, err
	}
	if now.Unix() > t.Expires {
		return Ticket{}, errors.New("ticket expired")
	}
	return t, nil
}
