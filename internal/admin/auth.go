package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/accounts"
	"github.com/famfamfam/simple-mining-proxy/internal/audit"
)

const (
	cookieName      = "smp_session"
	sessionLifetime = 12 * time.Hour
	// maxHashing bounds the password hashes running at once: each one costs
	// a fifth of a second of CPU, and a flood of logins must not starve the
	// proxy of it.
	maxHashing = 4
)

// principal is who a request is made by, and what they may do. It is
// computed on every request from the current state of the account, so a
// disabled account or a changed role takes effect at once, in sessions that
// are already open.
type principal struct {
	Kind     string // audit.ActorEnvAdmin, ActorUser or ActorAPIToken
	UserID   string
	Username string
	Role     accounts.Role
	Language string
	perms    accounts.Set
}

func (p *principal) Can(perm accounts.Permission) bool {
	return perm == "" || p.perms.Has(perm)
}

func (p *principal) actor(ip string) audit.Actor {
	return audit.Actor{Type: p.Kind, Name: p.Username, ID: p.UserID, IP: ip}
}

type ctxKey struct{}

func principalOf(r *http.Request) *principal {
	p, _ := r.Context().Value(ctxKey{}).(*principal)
	return p
}

func withPrincipal(r *http.Request, p *principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, p))
}

type webSession struct {
	userID  string // "" for the environment administrator
	issued  time.Time
	expires time.Time
}

// auth checks logins: a cookie session for the UI and a Bearer token
// for scripts. Every password and token check goes through the guard.
type auth struct {
	username, password, token string
	guard                     *guard
	accounts                  *accounts.Store // nil: only the environment administrator exists
	hashing                   chan struct{}

	mu       sync.Mutex
	sessions map[string]webSession // id -> session
}

func newAuth(username, password, token string, g *guard, acc *accounts.Store) *auth {
	return &auth{username: username, password: password, token: token, guard: g, accounts: acc,
		hashing:  make(chan struct{}, maxHashing),
		sessions: map[string]webSession{}}
}

func equal(a, b string) bool {
	aHash := sha256.Sum256([]byte(a))
	bHash := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aHash[:], bHash[:]) == 1
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *auth) envAdmin() *principal {
	return &principal{Kind: audit.ActorEnvAdmin, Username: a.username, Role: accounts.RoleSuperAdmin, perms: accounts.All()}
}

func (a *auth) apiToken() *principal {
	return &principal{Kind: audit.ActorAPIToken, Username: "api-token", Role: accounts.RoleSuperAdmin, perms: accounts.All()}
}

func userPrincipal(u accounts.User) *principal {
	return &principal{Kind: audit.ActorUser, UserID: u.ID, Username: u.Username, Role: u.Role,
		Language: u.Language, perms: u.Permissions()}
}

// authenticate checks the Bearer token or the session cookie. A wrong
// Bearer token counts as a failed attempt, like a wrong password.
func (a *auth) authenticate(r *http.Request, now time.Time) (*principal, attemptResult) {
	if h := r.Header.Get("Authorization"); a.token != "" && strings.HasPrefix(h, "Bearer ") {
		res := a.guard.attempt(clientIP(r), now, func() bool {
			return equal(strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), a.token)
		})
		if !res.OK {
			return nil, res
		}
		return a.apiToken(), res
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, attemptResult{}
	}
	a.mu.Lock()
	sess, found := a.sessions[c.Value]
	if found && now.After(sess.expires) {
		delete(a.sessions, c.Value)
		found = false
	}
	a.mu.Unlock()
	if !found {
		return nil, attemptResult{}
	}
	if sess.userID == "" {
		return a.envAdmin(), attemptResult{OK: true}
	}
	if a.accounts == nil {
		return nil, attemptResult{}
	}
	u, ok := a.accounts.Get(sess.userID)
	if !ok || !u.Active() || sess.issued.Before(u.PasswordChangedAt) {
		// Deleted, disabled, or the password was changed after this session
		// was opened: the session ends here.
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
		return nil, attemptResult{}
	}
	return userPrincipal(u), attemptResult{OK: true}
}

// attemptLogin checks the credentials through the guard and, on success,
// opens a new session. The slow password hash of an account runs outside the
// guard's lock and only for an address that is not locked.
func (a *auth) attemptLogin(ip, username, password string, now time.Time) (string, *principal, attemptResult) {
	if res, blocked := a.guard.blocked(ip, now); blocked {
		return "", nil, res
	}
	// Compare both values every time so timing does not reveal which failed.
	envOK := equal(username, a.username) && equal(password, a.password)
	var who *principal
	if envOK {
		who = a.envAdmin()
	} else if a.accounts != nil && a.accounts.Count() > 0 {
		a.hashing <- struct{}{}
		u, ok := a.accounts.Authenticate(username, password)
		<-a.hashing
		if ok {
			who = userPrincipal(u)
		}
	}
	res := a.guard.attempt(ip, now, func() bool { return who != nil })
	if !res.OK {
		return "", nil, res
	}
	id := a.open(who.UserID, now)
	if id == "" {
		return "", nil, attemptResult{}
	}
	if who.UserID != "" {
		a.accounts.RecordLogin(who.UserID)
	}
	return id, who, res
}

// open starts a session for an account ("" is the environment administrator)
// and returns its cookie value.
func (a *auth) open(userID string, now time.Time) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	id := hex.EncodeToString(b)
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, s := range a.sessions {
		if now.After(s.expires) {
			delete(a.sessions, key)
		}
	}
	a.sessions[id] = webSession{userID: userID, issued: now, expires: now.Add(sessionLifetime)}
	return id
}

func (a *auth) logout(r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
}

// revoke ends every open session of the account.
func (a *auth) revoke(userID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, s := range a.sessions {
		if s.userID == userID {
			delete(a.sessions, id)
		}
	}
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (a *auth) setCookie(w http.ResponseWriter, r *http.Request, id string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: id, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureRequest(r),
	})
}

// jsonOnly rejects state-changing requests without Content-Type:
// application/json. Such a request from another site needs a CORS preflight,
// which the proxy never allows, so this blocks CSRF together with SameSite.
func jsonOnly(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}
