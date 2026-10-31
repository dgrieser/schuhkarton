package auth

import (
	"path/filepath"
	"testing"
)

func TestAccounts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "users.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.HasAdmin() {
		t.Fatal("empty store has an admin")
	}
	if err := s.Create("Anna", "kurz", RoleAdmin, false); err != ErrBadPass {
		t.Fatalf("short password: %v", err)
	}
	if err := s.Create("Anna", "geheim123", RoleAdmin, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("anna ", "geheim123", RoleUser, true); err != ErrExists {
		t.Fatalf("duplicate (case, space): %v", err)
	}
	if err := s.Create("Ben", "passwort1", RoleUser, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login("ben", "passwort1", "1.2.3.4"); err != ErrPending {
		t.Fatalf("pending login: %v", err)
	}
	if s.Pending() != 1 {
		t.Fatal("pending count")
	}
	if err := s.Apply("Ben", Update{Approve: true}); err != nil {
		t.Fatal(err)
	}
	tok, u, err := s.Login("BEN", "passwort1", "1.2.3.4")
	if err != nil || u.Name != "Ben" {
		t.Fatalf("login: %v %v", u, err)
	}
	if got := s.Session(tok); got == nil || got.Name != "Ben" {
		t.Fatal("session lookup")
	}
	// survives a reload
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Session(tok); got == nil || got.Name != "Ben" {
		t.Fatal("session after reopen")
	}
	// last admin is protected
	admin := RoleUser
	if err := s.Apply("anna", Update{Role: &admin}); err != ErrLastAdmin {
		t.Fatalf("demote last admin: %v", err)
	}
	if err := s.Delete("Anna"); err != ErrLastAdmin {
		t.Fatalf("delete last admin: %v", err)
	}
	// disabling ends sessions
	yes := true
	if err := s.Apply("Ben", Update{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	if s.Session(tok) != nil {
		t.Fatal("disabled user keeps session")
	}
	if _, _, err := s.Login("Ben", "falsch!!", "1.2.3.4"); err != ErrBadLogin {
		t.Fatalf("wrong password: %v", err)
	}
	if _, _, err := s.Login("nobody", "whatever1", "1.2.3.4"); err != ErrBadLogin {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestPasswordChangeKeepsCurrentSession(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "users.json"))
	s.Create("A", "eins2345", RoleAdmin, false)
	t1, _, _ := s.Login("A", "eins2345", "x")
	t2, _, _ := s.Login("A", "eins2345", "y")
	if err := s.ChangePassword("A", "falsch123", "zwei2345", t1); err != ErrBadLogin {
		t.Fatalf("wrong old password: %v", err)
	}
	if err := s.ChangePassword("A", "eins2345", "zwei2345", t1); err != nil {
		t.Fatal(err)
	}
	if s.Session(t1) == nil || s.Session(t2) != nil {
		t.Fatal("password change: current session must stay, others end")
	}
}

func TestRateLimit(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "users.json"))
	s.Create("A", "eins2345", RoleAdmin, false)
	for i := 0; i < limitMax; i++ {
		s.Login("A", "nope-nope", "9.9.9.9")
	}
	if _, _, err := s.Login("A", "eins2345", "9.9.9.9"); err == nil || err == ErrBadLogin {
		t.Fatalf("expected block, got %v", err)
	}
	// another client is not blocked for that name
	if _, _, err := s.Login("A", "eins2345", "8.8.8.8"); err != nil {
		t.Fatalf("other client: %v", err)
	}
}

func TestClaimAdmin(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "users.json"))
	s.Create("Dora", "dora-pass", RoleUser, true)
	if err := s.ClaimAdmin("dora", "falsch-pass"); err != ErrBadLogin {
		t.Fatalf("wrong password: %v", err)
	}
	if err := s.ClaimAdmin("nobody", "dora-pass"); err != ErrBadLogin {
		t.Fatalf("unknown: %v", err)
	}
	if err := s.ClaimAdmin("dora", "dora-pass"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.Get("Dora")
	if !u.IsAdmin() {
		t.Fatalf("not an active admin: %+v", u)
	}
}
