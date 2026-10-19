package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func init() { iterations = 2000 } // the production count takes a fifth of a second per hash

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts.json")
	s, err := Open(path, "Admin")
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

const goodPassword = "correct horse battery"

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, goodPassword) {
		t.Fatal("the hash contains the password")
	}
	if ok, err := CheckPassword(goodPassword, h); !ok || err != nil {
		t.Fatalf("right password: %v %v", ok, err)
	}
	if ok, _ := CheckPassword(goodPassword+"x", h); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword(goodPassword)
	if h == h2 {
		t.Fatal("two hashes of one password are identical: the salt is not random")
	}
	for _, bad := range []string{"", "plain", "pbkdf2-sha256$1$AA$AA", "pbkdf2-sha256$x$AA$AA", "md5$2000$AAAAAAAAAAAAAAAAAAAAAA$AA"} {
		if ok, err := CheckPassword(goodPassword, bad); ok || err == nil {
			t.Errorf("damaged hash %q gave ok=%v err=%v", bad, ok, err)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	for pw, want := range map[string]string{
		"short":                  "password_short",
		strings.Repeat("a", 129): "password_long",
		"aaaaaaaaaaaaaaaa":       "password_weak",
		"operator.alice1":        "", // fine
		goodPassword:             "",
	} {
		if got := PasswordProblem(pw, "someone"); got != want {
			t.Errorf("%q: got %q, want %q", pw, got, want)
		}
	}
	if PasswordProblem("operator.alice", "Operator.Alice") != "password_username" {
		t.Error("a password equal to the username must be refused")
	}
}

func TestEffectivePermissions(t *testing.T) {
	op := Effective(RoleOperator, nil, nil)
	if !op.Has(StatsView) || !op.Has(DevicesManage) || op.Has(UsersManage) || op.Has(SettingsEdit) || op.Has(BillingManage) {
		t.Fatalf("operator defaults: %v", op.List())
	}
	op = Effective(RoleOperator, []Permission{SettingsEdit}, []Permission{DevicesManage})
	if !op.Has(SettingsEdit) || op.Has(DevicesManage) {
		t.Fatalf("grant/revoke: %v", op.List())
	}
	// A super administrator cannot be limited.
	sa := Effective(RoleSuperAdmin, nil, []Permission{UsersManage})
	if !sa.Has(UsersManage) {
		t.Fatal("revoke removed a super administrator's permission")
	}
	// Customers have no accounts: a role that is not known has no permission,
	// whatever is granted to it.
	if c := Effective("customer", []Permission{UsersManage, StatsView}, nil); len(c) != 0 {
		t.Fatalf("an unknown role has permissions: %v", c.List())
	}
	if len(All()) != len(StaffPermissions) {
		t.Fatal("All() is not every permission")
	}
}

func TestCreateAuthenticatePersist(t *testing.T) {
	s, path := open(t)
	u, err := s.Create(NewUser{Username: " Alice ", DisplayName: "Alice", Role: RoleOperator, Password: goodPassword, Language: "fa"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "alice" || u.Hash == "" || strings.Contains(u.Hash, goodPassword) {
		t.Fatalf("created %+v", u)
	}
	if got, ok := s.Authenticate("ALICE", goodPassword); !ok || got.ID != u.ID {
		t.Fatal("the new account cannot sign in")
	}
	if _, ok := s.Authenticate("alice", "wrong password!!"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, ok := s.Authenticate("nobody", goodPassword); ok {
		t.Fatal("unknown user accepted")
	}

	// A restart keeps the account.
	s2, err := Open(path, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Authenticate("alice", goodPassword); !ok {
		t.Fatal("the account is gone after reopening the file")
	}
}

func TestCreateValidation(t *testing.T) {
	s, _ := open(t)
	cases := []struct {
		name string
		in   NewUser
		key  string
	}{
		{"short name", NewUser{Username: "ab", Role: RoleOperator, Password: goodPassword}, "username_invalid"},
		{"bad chars", NewUser{Username: "a b c", Role: RoleOperator, Password: goodPassword}, "username_invalid"},
		{"env admin name", NewUser{Username: "admin", Role: RoleOperator, Password: goodPassword}, "username_taken"},
		{"weak password", NewUser{Username: "bob", Role: RoleOperator, Password: "short"}, "password_short"},
		{"unknown role", NewUser{Username: "bob", Role: "root", Password: goodPassword}, "role_unknown"},
		{"customer role", NewUser{Username: "bob", Role: "customer", Password: goodPassword}, "role_unknown"},
		{"unknown permission", NewUser{Username: "bob", Role: RoleOperator, Password: goodPassword, Grant: []Permission{"rm.rf"}}, "permission_unknown"},
		{"unknown language", NewUser{Username: "bob", Role: RoleOperator, Password: goodPassword, Language: "xx"}, "language_unknown"},
	}
	for _, c := range cases {
		_, err := s.Create(c.in)
		var e *Error
		if !errors.As(err, &e) || e.Key != c.key {
			t.Errorf("%s: got %v, want key %s", c.name, err, c.key)
		}
	}
	if _, err := s.Create(NewUser{Username: "bob", Role: RoleOperator, Password: goodPassword}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(NewUser{Username: "BOB", Role: RoleOperator, Password: goodPassword}); err == nil {
		t.Fatal("a duplicate username was accepted")
	}
	if s.Count() != 1 {
		t.Fatalf("failed creates left %d accounts", s.Count())
	}
}

func TestDisableAndPasswordChange(t *testing.T) {
	s, _ := open(t)
	u, _ := s.Create(NewUser{Username: "carol", Role: RoleOperator, Password: goodPassword})

	off := true
	if _, err := s.Update(u.ID, Update{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Authenticate("carol", goodPassword); ok {
		t.Fatal("a disabled account signed in")
	}
	on := false
	if _, err := s.Update(u.ID, Update{Disabled: &on}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Authenticate("carol", goodPassword); !ok {
		t.Fatal("re-enabled account cannot sign in")
	}

	before, _ := s.Get(u.ID)
	if err := s.SetPassword(u.ID, "a brand new password"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(u.ID)
	if !after.PasswordChangedAt.After(before.PasswordChangedAt) && !after.PasswordChangedAt.Equal(before.PasswordChangedAt) {
		t.Fatal("PasswordChangedAt did not move")
	}
	if _, ok := s.Authenticate("carol", goodPassword); ok {
		t.Fatal("the old password still works")
	}
	if _, ok := s.Authenticate("carol", "a brand new password"); !ok {
		t.Fatal("the new password does not work")
	}
	if err := s.SetPassword(u.ID, "short"); err == nil {
		t.Fatal("a weak password was accepted")
	}
	if err := s.SetPassword("nope", goodPassword); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestUpdateRoleAndDelete(t *testing.T) {
	s, _ := open(t)
	u, _ := s.Create(NewUser{Username: "dave", Role: RoleOperator, Password: goodPassword})
	r := RoleSuperAdmin
	got, err := s.Update(u.ID, Update{Role: &r})
	if err != nil || got.Role != RoleSuperAdmin {
		t.Fatalf("promote: %v %+v", err, got)
	}
	bad := Role("god")
	if _, err := s.Update(u.ID, Update{Role: &bad}); err == nil {
		t.Fatal("unknown role accepted")
	}
	if cur, _ := s.Get(u.ID); cur.Role != RoleSuperAdmin {
		t.Fatal("a refused update changed the account")
	}
	if err := s.Delete(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(u.ID); ok {
		t.Fatal("deleted account is still there")
	}
	if err := s.Delete(u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// Every problem key that the package can return has an English text, which the
// API sends and the web UI's locale test checks for translations.
func TestMessagesCoverEveryKey(t *testing.T) {
	var src strings.Builder
	for _, f := range []string{"store.go", "password.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	keys := map[string]bool{}
	for _, re := range []string{`bad\([^,]+, "([a-z_]+)"`, `return "([a-z_]+)"`} {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(src.String(), -1) {
			keys[m[1]] = true
		}
	}
	if len(keys) < 10 {
		t.Fatalf("found only %d keys: the pattern is out of date", len(keys))
	}
	for k := range keys {
		if Messages[k] == "" {
			t.Errorf("problem key %q has no English message", k)
		}
	}
	for k := range Messages {
		if !keys[k] {
			t.Errorf("message %q is not used by any problem", k)
		}
	}
}
