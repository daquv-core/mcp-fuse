package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef-test")

func tmpFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTicketsExpireAndCannotBeForged(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Now()
	tok := SignTicket(secret, Ticket{Person: "p", Backend: "b", Expires: now.Add(time.Minute).Unix()})
	if got, err := VerifyTicket(secret, tok, now); err != nil || got.Person != "p" || got.Backend != "b" {
		t.Fatalf("valid ticket: %v %v", got, err)
	}
	if _, err := VerifyTicket(secret, tok, now.Add(2*time.Minute)); err == nil {
		t.Error("an expired ticket was accepted")
	}
	if _, err := VerifyTicket([]byte("another secret, also long enough for this"), tok, now); err == nil {
		t.Error("a ticket signed with another secret was accepted")
	}
	body, sig, _ := strings.Cut(tok, ".")
	if _, err := VerifyTicket(secret, body+"x."+sig, now); err == nil {
		t.Error("a ticket with a changed body was accepted")
	}
}

func TestSecretMustBeLongEnough(t *testing.T) {
	if _, err := ReadSecret(tmpFile(t, "short")); err == nil {
		t.Error("a short secret was accepted")
	}
	if s, err := ReadSecret(tmpFile(t, "0123456789abcdef0123456789abcdef\n")); err != nil || len(s) != 32 {
		t.Errorf("a long enough secret: %v %d", err, len(s))
	}
}

func TestStateFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.bin")
	st, err := Open(path, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put("p", "b", Grant{Access: "a"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, %v", info.Mode().Perm(), err)
	}
}
