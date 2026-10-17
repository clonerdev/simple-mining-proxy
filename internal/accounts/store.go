package accounts

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/store"
)

// Languages an account may prefer, for the panel and for the Telegram bot.
var Languages = []string{"en", "ru", "fa"}

const (
	maxUsers       = 1000
	maxDisplayName = 64
	maxGrants      = 16
)

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)

// User is one account. Hash is kept in the file and never leaves the server.
type User struct {
	ID          string       `json:"id"`
	Username    string       `json:"username"`
	DisplayName string       `json:"display_name,omitempty"`
	Role        Role         `json:"role"`
	Grant       []Permission `json:"grant,omitempty"`
	Revoke      []Permission `json:"revoke,omitempty"`
	// CustomerID links a customer account to its customer record (the devices
	// module); it is empty for staff.
	CustomerID string `json:"customer_id,omitempty"`
	Language   string `json:"language,omitempty"`
	Disabled   bool   `json:"disabled,omitempty"`
	Hash       string `json:"hash"`

	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	PasswordChangedAt time.Time `json:"password_changed_at"`
	LastLoginAt       time.Time `json:"last_login_at,omitempty"`
}

// Permissions is what the account may do now.
func (u User) Permissions() Set { return Effective(u.Role, u.Grant, u.Revoke) }

// Active reports whether the account may sign in and keep a session.
func (u User) Active() bool { return !u.Disabled }

type file struct {
	Users []User `json:"users"`
}

// Store is the accounts file.
type Store struct {
	doc      *store.Doc[file]
	reserved map[string]bool
	now      func() time.Time

	mu    sync.RWMutex
	index map[string]User // by id, a copy of the document for lock-free-ish reads
	names map[string]string
}

// Open loads the accounts file at path. reserved names (the environment
// administrator) cannot be taken by an account.
func Open(path string, reserved ...string) (*Store, error) {
	doc, err := store.OpenDoc[file](path, nil)
	if err != nil {
		return nil, err
	}
	s := &Store{doc: doc, reserved: map[string]bool{}, now: time.Now}
	for _, r := range reserved {
		s.reserved[strings.ToLower(strings.TrimSpace(r))] = true
	}
	doc.Read(func(f *file) { s.reindex(f) })
	return s, nil
}

func (s *Store) reindex(f *file) {
	idx := make(map[string]User, len(f.Users))
	names := make(map[string]string, len(f.Users))
	for _, u := range f.Users {
		idx[u.ID] = u
		names[u.Username] = u.ID
	}
	s.mu.Lock()
	s.index, s.names = idx, names
	s.mu.Unlock()
}

// Error is a problem with a request that the caller can show to the operator.
// Key is a stable message key, Field the input it is about.
type Error struct {
	Key    string
	Field  string
	Params map[string]any
}

func (e *Error) Error() string { return "accounts: " + e.Key }

// ErrNotFound is returned for an unknown id.
var ErrNotFound = errors.New("accounts: user not found")

func bad(field, key string, kv ...any) *Error {
	e := &Error{Key: key, Field: field}
	if len(kv) > 0 {
		e.Params = map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			e.Params[kv[i].(string)] = kv[i+1]
		}
	}
	return e
}

// Count returns the number of accounts.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.index)
}

// List returns the accounts ordered by username.
func (s *Store) List() []User {
	s.mu.RLock()
	out := make([]User, 0, len(s.index))
	for _, u := range s.index {
		out = append(out, u)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// Get returns the account with the id.
func (s *Store) Get(id string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.index[id]
	return u, ok
}

// ByUsername returns the account with the (case-insensitive) username.
func (s *Store) ByUsername(name string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.names[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return User{}, false
	}
	return s.index[id], true
}

// NewUser is a request to create an account.
type NewUser struct {
	Username    string
	DisplayName string
	Role        Role
	Grant       []Permission
	Revoke      []Permission
	CustomerID  string
	Language    string
	Password    string
}

func normalizePermissions(field string, in []Permission) ([]Permission, *Error) {
	if len(in) > maxGrants {
		return nil, bad(field, "permissions_many", "max", maxGrants)
	}
	seen := map[Permission]bool{}
	var out []Permission
	for _, p := range in {
		if !p.Valid() {
			return nil, bad(field, "permission_unknown", "permission", string(p))
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func checkCommon(role Role, display, lang, customerID string) *Error {
	if !role.Valid() {
		return bad("role", "role_unknown")
	}
	if len([]rune(display)) > maxDisplayName {
		return bad("display_name", "display_name_long", "max", maxDisplayName)
	}
	if lang != "" {
		ok := false
		for _, l := range Languages {
			ok = ok || l == lang
		}
		if !ok {
			return bad("language", "language_unknown")
		}
	}
	if role == RoleCustomer && customerID == "" {
		return bad("customer_id", "customer_required")
	}
	if role != RoleCustomer && customerID != "" {
		return bad("customer_id", "customer_staff_only")
	}
	return nil
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Create adds an account.
func (s *Store) Create(in NewUser) (User, error) {
	name := strings.ToLower(strings.TrimSpace(in.Username))
	switch {
	case !usernameRe.MatchString(name):
		return User{}, bad("username", "username_invalid")
	case s.reserved[name]:
		return User{}, bad("username", "username_taken")
	}
	if e := checkCommon(in.Role, in.DisplayName, in.Language, in.CustomerID); e != nil {
		return User{}, e
	}
	if k := PasswordProblem(in.Password, name); k != "" {
		return User{}, bad("password", k, "min", MinPasswordLen, "max", MaxPasswordLen)
	}
	grant, e := normalizePermissions("grant", in.Grant)
	if e != nil {
		return User{}, e
	}
	revoke, e := normalizePermissions("revoke", in.Revoke)
	if e != nil {
		return User{}, e
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC()
	u := User{
		ID: newID(), Username: name, DisplayName: strings.TrimSpace(in.DisplayName), Role: in.Role,
		Grant: grant, Revoke: revoke, CustomerID: in.CustomerID, Language: in.Language,
		Hash: hash, CreatedAt: now, UpdatedAt: now, PasswordChangedAt: now,
	}
	err = s.doc.Mutate(func(f *file) error {
		if len(f.Users) >= maxUsers {
			return bad("", "users_many", "max", maxUsers)
		}
		for _, x := range f.Users {
			if x.Username == name {
				return bad("username", "username_taken")
			}
		}
		f.Users = append(f.Users, u)
		return nil
	}, s.reindex)
	return u, err
}

// Update changes an account; nil fields stay as they are.
type Update struct {
	DisplayName *string
	Role        *Role
	Grant       *[]Permission
	Revoke      *[]Permission
	CustomerID  *string
	Language    *string
	Disabled    *bool
}

// Update applies u to the account with the id.
func (s *Store) Update(id string, in Update) (User, error) {
	var out User
	err := s.doc.Mutate(func(f *file) error {
		i := indexOf(f, id)
		if i < 0 {
			return ErrNotFound
		}
		u := f.Users[i]
		if in.DisplayName != nil {
			u.DisplayName = strings.TrimSpace(*in.DisplayName)
		}
		if in.Role != nil {
			u.Role = *in.Role
		}
		if in.CustomerID != nil {
			u.CustomerID = *in.CustomerID
		}
		if in.Language != nil {
			u.Language = *in.Language
		}
		if in.Disabled != nil {
			u.Disabled = *in.Disabled
		}
		var e *Error
		if in.Grant != nil {
			if u.Grant, e = normalizePermissions("grant", *in.Grant); e != nil {
				return e
			}
		}
		if in.Revoke != nil {
			if u.Revoke, e = normalizePermissions("revoke", *in.Revoke); e != nil {
				return e
			}
		}
		if e := checkCommon(u.Role, u.DisplayName, u.Language, u.CustomerID); e != nil {
			return e
		}
		u.UpdatedAt = s.now().UTC()
		f.Users[i] = u
		out = u
		return nil
	}, s.reindex)
	return out, err
}

// SetPassword replaces the password. Sessions opened before this moment are
// no longer valid: see User.PasswordChangedAt.
func (s *Store) SetPassword(id, password string) error {
	hash, err := func() (string, error) {
		u, ok := s.Get(id)
		if !ok {
			return "", ErrNotFound
		}
		if k := PasswordProblem(password, u.Username); k != "" {
			return "", bad("password", k, "min", MinPasswordLen, "max", MaxPasswordLen)
		}
		return HashPassword(password)
	}()
	if err != nil {
		return err
	}
	return s.doc.Mutate(func(f *file) error {
		i := indexOf(f, id)
		if i < 0 {
			return ErrNotFound
		}
		now := s.now().UTC()
		f.Users[i].Hash = hash
		f.Users[i].PasswordChangedAt = now
		f.Users[i].UpdatedAt = now
		return nil
	}, s.reindex)
}

// Delete removes an account.
func (s *Store) Delete(id string) error {
	return s.doc.Mutate(func(f *file) error {
		i := indexOf(f, id)
		if i < 0 {
			return ErrNotFound
		}
		f.Users = append(f.Users[:i], f.Users[i+1:]...)
		return nil
	}, s.reindex)
}

func indexOf(f *file, id string) int {
	for i, u := range f.Users {
		if u.ID == id {
			return i
		}
	}
	return -1
}

// Authenticate checks a username and password. An unknown name costs as much
// time as a wrong password. A disabled account never authenticates, and it is
// not told apart from a wrong password.
func (s *Store) Authenticate(username, password string) (User, bool) {
	u, found := s.ByUsername(username)
	hash := u.Hash
	if !found {
		hash = dummy()
	}
	ok, err := CheckPassword(password, hash)
	if err != nil || !ok || !found || !u.Active() {
		return User{}, false
	}
	return u, true
}

// RecordLogin stores the time of a successful sign-in. A failure to save it
// is ignored: the sign-in itself is fine.
func (s *Store) RecordLogin(id string) {
	_ = s.doc.Mutate(func(f *file) error {
		i := indexOf(f, id)
		if i < 0 {
			return ErrNotFound
		}
		f.Users[i].LastLoginAt = s.now().UTC()
		return nil
	}, s.reindex)
}

var (
	dummyOnce sync.Once
	dummyVal  string
)

func dummy() string {
	dummyOnce.Do(func() {
		h, err := hashWith("not-a-real-password", make([]byte, saltLen), iterations)
		if err != nil {
			panic(err)
		}
		dummyVal = h
	})
	return dummyVal
}
