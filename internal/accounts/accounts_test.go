package accounts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web", "accounts.json")
	s, err := Open(path)
	if err != nil || !s.Empty() {
		t.Fatal(err)
	}
	if _, err := s.Create("bad name", RoleUser, "longenough1"); err == nil {
		t.Fatal("invalid name accepted")
	}
	if _, err := s.Create("alex", RoleUser, "short"); err == nil {
		t.Fatal("short password accepted")
	}
	admin, err := s.Create("marius", RoleAdmin, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("Marius", RoleUser, "another password"); err != ErrExists {
		t.Fatal("names must be unique ignoring case:", err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatal("store file mode", st.Mode())
	}
	if _, err := s.Authenticate("MARIUS", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"marius", "wrong password"}, {"nobody", "correct horse battery"}} {
		if _, err := s.Authenticate(c[0], c[1]); err != ErrBadPassword {
			t.Fatal(c, err)
		}
	}
	// The last admin cannot be demoted, disabled or deleted.
	if _, err := s.Update(admin.ID, func(u *User) error { u.Role = RoleUser; return nil }); err != ErrLastAdmin {
		t.Fatal(err)
	}
	if err := s.Delete(admin.ID); err != ErrLastAdmin {
		t.Fatal(err)
	}
	if u, _ := s.Get(admin.ID); u.Role != RoleAdmin {
		t.Fatal("failed update must not change the user")
	}

	// Passkeys.
	cred := &webauthn.Credential{ID: []byte{1, 2, 3}}
	if err := s.AddPasskey(admin.ID, "Laptop", cred); err != nil {
		t.Fatal(err)
	}
	cred.Authenticator.SignCount = 7
	if err := s.UsedPasskey(admin.ID, cred); err != nil {
		t.Fatal(err)
	}
	u, _ := s.Get(admin.ID)
	if len(u.Passkeys) != 1 || u.Passkeys[0].Credential.Authenticator.SignCount != 7 || u.LastLogin.IsZero() {
		t.Fatalf("%+v", u.Passkeys[0])
	}
	if id, ok := UserIDFromHandle(u.WebAuthnID()); !ok || id != admin.ID {
		t.Fatal("user handle")
	}
	// Passkey-only accounts.
	if err := s.SetPassword(admin.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePasskey(admin.ID, u.Passkeys[0].ID); err == nil {
		t.Fatal("removing the only way to sign in")
	}
	if _, err := s.Authenticate("marius", "correct horse battery"); err != ErrBadPassword {
		t.Fatal("password still works after removal")
	}

	// Persistence.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := s2.ByName("marius"); err != nil || len(u.Passkeys) != 1 || u.HasPassword() {
		t.Fatal(u, err)
	}
	user, _ := s2.Create("alex", RoleUser, "")
	if err := s2.Delete(user.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPasswordHash(t *testing.T) {
	h := HashPassword("secret password")
	if !VerifyPassword(h, "secret password") || VerifyPassword(h, "secret passwore") || VerifyPassword("garbage", "x") {
		t.Fatal("verify")
	}
	if HashPassword("same") == HashPassword("same") {
		t.Fatal("hashes must be salted")
	}
}

func TestReloadAfterExternalChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	daemon, _ := Open(path)
	if _, err := daemon.Create("admin", RoleAdmin, "first password"); err != nil {
		t.Fatal(err)
	}
	cli, _ := Open(path)
	if _, err := cli.Create("alex", RoleUser, "second password"); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.ByName("alex"); err != nil {
		t.Fatal("daemon does not see the user added by the CLI:", err)
	}
	if _, err := daemon.Create("third", RoleUser, "third password"); err != nil {
		t.Fatal(err)
	}
	if len(cli.List()) != 3 {
		t.Fatal("writes overwrote each other")
	}
}
