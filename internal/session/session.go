// Package session relays one ASIC connection to one pool connection and
// keeps the registry of live sessions.
package session

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/opshare"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

// Upstream is a connected pool with the pool settings captured at pick time:
// later edits of the pool apply to new sessions only.
type Upstream struct {
	Conn     net.Conn
	PoolID   string
	PoolName string
	Addr     string // host:port of the pool server
	Template string
	Password string
	// OperatorShare, OperatorUser and OperatorPassword describe the pool's
	// permission for the operator's second worker and the operator's login.
	OperatorShare    bool
	OperatorUser     string
	OperatorPassword string
	OnFailure        func(UpstreamFailure, error)
}

type UpstreamFailure string

const (
	AuthorizationRejected UpstreamFailure = "authorization_rejected"
	JobTimeout            UpstreamFailure = "job_timeout"
)

type upstreamError struct {
	kind UpstreamFailure
	err  error
}

func (e *upstreamError) Error() string { return e.err.Error() }

// Router picks and dials a pool for a new session.
type Router interface {
	Pick(ctx context.Context) (*Upstream, error)
}

const (
	writeTimeout    = 30 * time.Second
	pendingMaxAge   = 60 * time.Second
	sweepInterval   = 10 * time.Second
	maxPendingIDs   = 10000
	maxPendingAuth  = 128
	maxTrackedLogin = 128
	maxLoginLen     = 256  // real ASIC logins are far shorter; bounds per-session memory
	maxDifficulty   = 1e30 // far above SHA-256 network difficulty; keeps statistics finite
)

var (
	errPoolReconnect = errors.New("pool sent client.reconnect")
	errLoginTooLong  = fmt.Errorf("miner login longer than %d bytes", maxLoginLen)
)

type submitInfo struct {
	at     time.Time
	diff   float64
	worker string // ASIC login the share was sent with

	// The operator's share: track says the share is accounted for it,
	// operator that it was credited to the operator's worker, in the window
	// that starts at window.
	track      bool
	operator   bool
	window     int64
	deviceID   string
	customerID string
}

type authInfo struct {
	at   time.Time
	user string
}

type Session struct {
	ID          uint64
	RemoteAddr  string
	IP          string
	Transport   string
	ConnectedAt time.Time

	miner net.Conn
	up    *Upstream
	srv   *Server

	closeOnce   sync.Once
	closeReason string

	mu            sync.Mutex
	userAgent     string
	worker        string
	upstreamUser  string
	difficulty    float64
	accepted      uint64
	rejected      uint64
	lastShare     time.Time // last accepted share
	lastSubmit    time.Time // last share the ASIC sent, whatever the pool answers
	rate          *stats.Rate
	logins        map[string]string   // ASIC login -> upstream user
	pendingAuth   map[string]authInfo // request id -> authorization metadata
	pendingSubmit map[string]submitInfo
	lastSweep     time.Time

	// authorized holds the logins the Gate allowed (login -> device), and
	// devices the devices among them. Both stay empty without device control.
	authorized map[string]authz
	devices    map[string]struct{}

	// opAsked is set once the operator's worker was sent to the pool, opReady
	// once the pool accepted it. Guarded by mu.
	opAsked bool
	opReady bool

	// maxLine is max_line_bytes captured at connect time, like the line
	// readers, so one session never mixes two limits.
	maxLine int

	deadlineMu        sync.Mutex
	lastMinerActivity time.Time
	lastPoolActivity  time.Time
	authorizedAt      time.Time
	lastJob           time.Time
}

func (s *Session) PoolID() string { return s.up.PoolID }

func (s *Session) PoolAddr() string { return s.up.Addr }

// Worker is the ASIC login of the session, "" before mining.authorize.
func (s *Session) Worker() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.worker
}

// Close ends the session: both connections are closed and both relay
// goroutines return. Safe to call many times from any goroutine.
func (s *Session) Close(reason string) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closeReason = reason
		s.mu.Unlock()
		s.miner.Close() // tls.Conn sends close_notify first
		s.up.Conn.Close()
	})
}

// Info is a row of the miners table.
type Info struct {
	ID           uint64     `json:"id"`
	Worker       string     `json:"worker"`
	UpstreamUser string     `json:"upstream_user"`
	IP           string     `json:"ip"`
	Transport    string     `json:"transport"`
	PoolID       string     `json:"pool_id"`
	PoolName     string     `json:"pool_name"`
	PoolAddr     string     `json:"pool_addr"`
	UserAgent    string     `json:"user_agent"`
	Difficulty   float64    `json:"difficulty"`
	Accepted     uint64     `json:"accepted"`
	Rejected     uint64     `json:"rejected"`
	LastShare    *time.Time `json:"last_share"`
	LastSubmit   *time.Time `json:"last_submit"`
	HashrateHs   float64    `json:"hashrate_hs"`
	ConnectedAt  time.Time  `json:"connected_at"`
	CustomerID   string     `json:"customer_id,omitempty"`
	DeviceID     string     `json:"device_id,omitempty"`
}

func (s *Session) Info(now time.Time) Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := Info{
		ID: s.ID, Worker: s.worker, UpstreamUser: s.upstreamUser, IP: s.IP, Transport: s.Transport,
		PoolID: s.up.PoolID, PoolName: s.up.PoolName, PoolAddr: s.up.Addr, UserAgent: s.userAgent, Difficulty: s.difficulty,
		Accepted: s.accepted, Rejected: s.rejected, HashrateHs: s.rate.Hashrate(now), ConnectedAt: s.ConnectedAt,
	}
	for _, a := range s.authorized {
		in.CustomerID, in.DeviceID = a.customerID, a.deviceID
		break
	}
	if !s.lastShare.IsZero() {
		t := s.lastShare
		in.LastShare = &t
	}
	if !s.lastSubmit.IsZero() {
		t := s.lastSubmit
		in.LastSubmit = &t
	}
	return in
}

// Server turns accepted connections into sessions.
type Server struct {
	Settings *settings.Store
	Router   Router
	Registry *Registry
	Events   *events.Log
	Stats    *stats.Collector
	// Gate enforces device control; nil lets everybody in.
	Gate Gate
	// Splitter is the operator's optional share; nil never routes anything to
	// the operator.
	Splitter opshare.Splitter
	// TLSConfig returns the config for a new TLS connection; nil when the
	// TLS listener is off.
	TLSConfig func() *tls.Config

	mu     sync.Mutex
	nextID uint64
}

// Serve handles one accepted connection until the session ends. firstDone is
// called once the connection stops being "pending": after the first valid
// message or when it is closed before that.
func (srv *Server) Serve(ctx context.Context, conn net.Conn, isTLS bool, firstDone func()) {
	// Registered first so it runs last: the other defers still close the
	// connection and unregister the session.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in session", "remote", conn.RemoteAddr().String(), "panic", r, "stack", string(debug.Stack()))
		}
	}()
	// A closure, not `defer conn.Close()`: conn is replaced by the tls.Conn
	// below, and closing the wrapper is what sends close_notify.
	defer func() { conn.Close() }()
	// On shutdown, abort any phase that is not ctx-aware (waiting for the
	// first message, relaying). Closing the raw socket is enough to unblock.
	raw := conn
	stopAbort := context.AfterFunc(ctx, func() { raw.Close() })
	defer stopAbort()
	var once sync.Once
	done := func() { once.Do(firstDone) }
	defer done()

	cfg := srv.Settings.Get()
	remote := conn.RemoteAddr().String()
	ip, _, _ := net.SplitHostPort(remote)
	log := slog.With("remote", remote)

	transport := "tcp"
	if isTLS {
		transport = "tls"
		tc := tls.Server(conn, srv.TLSConfig())
		conn.SetDeadline(time.Now().Add(cfg.TLSHandshakeTimeout))
		if err := tc.HandshakeContext(ctx); err != nil {
			srv.Stats.TLSHandshakeFailed()
			log.Debug("tls handshake failed", "err", err)
			// One event per IP every 10 minutes: enough to spot an ASIC whose
			// firmware rejects the certificate or TLS version, without flooding.
			srv.Events.Throttled("tls_handshake:"+ip, 10*time.Minute, "tls_handshake_failed",
				"TLS handshake with %s failed: %v", ip, err)
			return
		}
		conn = tc
	}

	conn.SetDeadline(time.Now().Add(cfg.FirstMessageTimeout))
	lr := stratum.NewLineReader(conn, cfg.MaxLineBytes())
	line, err := lr.ReadLine()
	if err != nil {
		log.Debug("no first message", "err", err)
		return
	}
	msg, err := stratum.Parse(line)
	if err != nil || msg.Method == "" {
		log.Debug("first message is not a stratum request", "err", err)
		return
	}
	// The line slice is reused by the reader; keep a copy until it is sent.
	first := append([]byte(nil), line...)
	done()
	conn.SetDeadline(time.Time{})

	gated := srv.Gate != nil && srv.Gate.Active()
	if gated && !srv.Gate.AllowConnect(ip) {
		log.Debug("connection refused by device control", "reason", "too many refused logins")
		return
	}

	up, err := srv.Router.Pick(ctx)
	if err != nil {
		srv.Events.Throttled("no_pool", 30*time.Second, "no_pool", "no pool available, closing miner connections: %v", err)
		return
	}

	srv.mu.Lock()
	srv.nextID++
	id := srv.nextID
	srv.mu.Unlock()
	now := time.Now()
	s := &Session{
		ID: id, RemoteAddr: remote, IP: ip, Transport: transport, ConnectedAt: now,
		miner: conn, up: up, srv: srv,
		rate:              stats.NewRate(now),
		logins:            map[string]string{},
		pendingAuth:       map[string]authInfo{},
		pendingSubmit:     map[string]submitInfo{},
		authorized:        map[string]authz{},
		devices:           map[string]struct{}{},
		maxLine:           cfg.MaxLineBytes(),
		lastMinerActivity: now,
		lastPoolActivity:  now,
	}
	srv.Registry.add(s)
	defer srv.Registry.remove(s)
	if ctx.Err() != nil { // shutdown began while the pool was being dialed
		s.Close("shutdown")
		return
	}
	log = log.With("session", id, "pool", up.PoolID)
	log.Debug("session started", "transport", transport)

	if gated {
		// A connection that does not get a login through the Gate in time is
		// closed: an unknown client must not hold a pool connection open.
		defer s.watchAuthorization(cfg.DeviceAuthTimeout)()
	}
	err = s.relay(first, msg, lr)
	s.Close(closeReason(err))
	s.mu.Lock()
	reason := s.closeReason
	s.mu.Unlock()
	log.Debug("session closed", "reason", reason, "worker", s.worker)
}

func closeReason(err error) string {
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return "connection closed"
	case errors.Is(err, net.ErrClosed):
		return "closed by proxy"
	}
	return err.Error()
}

func (s *Session) relay(first []byte, firstMsg *stratum.Message, minerReader *stratum.LineReader) error {
	out, err := s.forwardFromMiner(first, firstMsg)
	if err != nil {
		return err
	}
	if out != nil {
		if err := writeLine(s.up.Conn, out); err != nil {
			return fmt.Errorf("write to pool: %w", err)
		}
	}
	errc := make(chan error, 2)
	go func() { errc <- guard(func() error { return s.minerLoop(minerReader) }) }()
	go func() { errc <- guard(func() error { return s.poolLoop(stratum.NewLineReader(s.up.Conn, s.maxLine)) }) }()
	err = <-errc
	s.Close(closeReason(err))
	<-errc
	return err
}

// guard turns a panic in a relay goroutine into an error, so a bug reachable
// from one connection's bytes ends that session, not the whole proxy.
func guard(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in session relay", "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return fn()
}

func writeLine(c net.Conn, b []byte) error {
	c.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := c.Write(b)
	return err
}

func (s *Session) prepareMinerRead() {
	s.deadlineMu.Lock()
	s.miner.SetReadDeadline(s.lastMinerActivity.Add(s.srv.Settings.Get().MinerIdleTimeout))
	s.deadlineMu.Unlock()
}

func (s *Session) preparePoolRead() {
	s.deadlineMu.Lock()
	s.up.Conn.SetReadDeadline(s.poolDeadlineLocked(s.srv.Settings.Get()))
	s.deadlineMu.Unlock()
}

func (s *Session) jobBaseLocked() time.Time {
	if s.authorizedAt.IsZero() {
		return time.Time{}
	}
	if s.lastJob.After(s.authorizedAt) {
		return s.lastJob
	}
	return s.authorizedAt
}

func (s *Session) poolDeadlineLocked(v *settings.Values) time.Time {
	if base := s.jobBaseLocked(); !base.IsZero() {
		// Settings keep the job timeout at most as long as the idle timeout.
		// Use the job deadline after authorization so an equal idle timeout
		// cannot close the session just before its pool failure is reported.
		return base.Add(v.UpstreamJobTimeout)
	}
	return s.lastPoolActivity.Add(v.UpstreamIdleTimeout)
}

func (s *Session) jobTimedOut(now time.Time) bool {
	s.deadlineMu.Lock()
	defer s.deadlineMu.Unlock()
	base := s.jobBaseLocked()
	return !base.IsZero() && !now.Before(base.Add(s.srv.Settings.Get().UpstreamJobTimeout))
}

func (s *Session) reportFailure(err *upstreamError) {
	// A shutdown or an operator's drain must not turn into a pool failure.
	s.mu.Lock()
	closed := s.closeReason != ""
	s.mu.Unlock()
	if !closed && s.up.OnFailure != nil {
		s.up.OnFailure(err.kind, err.err)
	}
}

func (s *Session) markMinerActivity(now time.Time) {
	s.deadlineMu.Lock()
	s.lastMinerActivity = now
	s.deadlineMu.Unlock()
}

func (s *Session) markPoolActivity(now time.Time) {
	s.deadlineMu.Lock()
	s.lastPoolActivity = now
	s.deadlineMu.Unlock()
}

// refreshIdleDeadlines makes runtime timeout changes affect reads that are
// already blocked, using the time of the last received message as the base.
func (s *Session) refreshIdleDeadlines(v *settings.Values) {
	s.deadlineMu.Lock()
	s.miner.SetReadDeadline(s.lastMinerActivity.Add(v.MinerIdleTimeout))
	s.up.Conn.SetReadDeadline(s.poolDeadlineLocked(v))
	s.deadlineMu.Unlock()
}

// minerLoop relays ASIC → pool.
func (s *Session) minerLoop(lr *stratum.LineReader) error {
	for {
		s.prepareMinerRead()
		line, err := lr.ReadLine()
		if err != nil {
			return fmt.Errorf("miner: %w", err)
		}
		s.markMinerActivity(time.Now())
		msg, err := stratum.Parse(line)
		if err != nil {
			return fmt.Errorf("miner sent invalid JSON: %w", err)
		}
		out, err := s.forwardFromMiner(line, msg)
		if err != nil {
			return err
		}
		if out == nil { // answered here: the Gate refused it
			continue
		}
		if err := writeLine(s.up.Conn, out); err != nil {
			return fmt.Errorf("write to pool: %w", err)
		}
	}
}

// forwardFromMiner rewrites one ASIC message and enforces max_line_bytes on
// the result too: rewriting and JSON escaping can make a line longer.
func (s *Session) forwardFromMiner(line []byte, msg *stratum.Message) ([]byte, error) {
	out, err := s.fromMiner(line, msg)
	if err != nil {
		return nil, err
	}
	if len(out) > s.maxLine {
		return nil, fmt.Errorf("rewritten miner message is %d bytes, over max_line_bytes (%d)", len(out), s.maxLine)
	}
	return out, nil
}

// poolLoop relays pool → ASIC.
func (s *Session) poolLoop(lr *stratum.LineReader) error {
	for {
		if s.jobTimedOut(time.Now()) {
			failure := &upstreamError{kind: JobTimeout, err: fmt.Errorf("no mining.notify within %s", s.srv.Settings.Get().UpstreamJobTimeout)}
			s.reportFailure(failure)
			return failure
		}
		s.preparePoolRead()
		line, err := lr.ReadLine()
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) && s.jobTimedOut(time.Now()) {
				// Reuse the same path even when the pool has gone completely silent.
				continue
			}
			return fmt.Errorf("pool: %w", err)
		}
		s.markPoolActivity(time.Now())
		out, err := s.fromPool(line)
		if out != nil {
			if writeErr := writeLine(s.miner, out); writeErr != nil {
				if failure, ok := err.(*upstreamError); ok {
					s.reportFailure(failure)
				}
				return fmt.Errorf("write to miner: %w", writeErr)
			}
		}
		if err != nil {
			if failure, ok := err.(*upstreamError); ok {
				s.reportFailure(failure)
			}
			return err
		}
	}
}

// fromMiner returns the bytes to send to the pool for one ASIC message.
func (s *Session) fromMiner(line []byte, msg *stratum.Message) ([]byte, error) {
	switch msg.Method {
	case "mining.subscribe":
		if params, err := stratum.ParamsArray(msg.Params); err == nil {
			if ua, ok := stratum.StringAt(params, 0); ok {
				s.mu.Lock()
				s.userAgent = ua
				s.mu.Unlock()
			}
		}
	case "mining.authorize":
		return s.authorize(line, msg)
	case "mining.submit":
		return s.submit(line, msg)
	}
	return line, nil
}

func (s *Session) rewrite(login, asicPassword string, vars stratum.Vars) stratum.Rewrite {
	return stratum.RewriteWith(login, asicPassword, s.up.Template, s.up.Password, vars)
}

func (s *Session) authorize(line []byte, msg *stratum.Message) ([]byte, error) {
	params, err := stratum.ParamsArray(msg.Params)
	if err != nil {
		return line, nil
	}
	login, ok := stratum.StringAt(params, 0)
	if !ok {
		return line, nil
	}
	if len(login) > maxLoginLen {
		return nil, errLoginTooLong
	}
	asicPass, _ := stratum.StringAt(params, 1)
	var vars stratum.Vars
	var granted *authz
	if s.gateActive() {
		s.mu.Lock()
		ua := s.userAgent
		s.mu.Unlock()
		dec := s.srv.Gate.Authorize(AuthRequest{SessionID: s.ID, IP: s.IP, Transport: s.Transport, Login: login, Password: asicPass, UserAgent: ua})
		if dec.Allow && usesAccount(s.up.Template) && dec.Account == "" {
			dec = AuthDecision{Code: "no_account", Reason: "No pool account is set for this customer"}
		}
		if !dec.Allow {
			s.srv.Events.Throttled("denied:"+s.IP+":"+dec.Code, time.Minute, "login_refused",
				"login refused for %s from %s: %s", truncateLogin(login), s.IP, dec.Code)
			writeLine(s.miner, reply(msg.ID, false, dec.Reason))
			return nil, &deniedError{code: dec.Code, reason: dec.Reason}
		}
		// The ASIC's password is the customer's connection key: it is never
		// passed on to the pool.
		asicPass = ""
		vars = stratum.Vars{Account: dec.Account, Worker: dec.Worker}
		granted = &authz{deviceID: dec.DeviceID, customerID: dec.CustomerID}
		defer s.keepOnlyNewest(dec.DeviceID, dec.MaxConnections)
	} else if usesAccount(s.up.Template) {
		writeLine(s.miner, reply(msg.ID, false, "Pool account template needs device control"))
		return nil, &deniedError{code: "no_account", reason: "pool account template needs device control"}
	}
	rw := s.rewrite(login, asicPass, vars)

	now := time.Now()
	s.mu.Lock()
	s.sweepLocked(now)
	if _, exists := s.logins[login]; exists || len(s.logins) < maxTrackedLogin {
		s.logins[login] = rw.User
	}
	if key := msg.IDKey(); key != "" {
		if _, exists := s.pendingAuth[key]; exists || len(s.pendingAuth) < maxPendingAuth {
			s.pendingAuth[key] = authInfo{at: now, user: rw.User}
		}
	}
	s.worker, s.upstreamUser = login, rw.User
	if granted != nil {
		s.authorized[login] = *granted
		s.devices[granted.deviceID] = struct{}{}
	}
	s.mu.Unlock()

	params[0] = stratum.EncodeString(rw.User)
	if len(params) > 1 {
		params[1] = stratum.EncodeString(rw.Password)
	} else {
		params = append(params, stratum.EncodeString(rw.Password))
	}
	out, err := stratum.ReplaceParams(line, params)
	if err != nil || granted == nil {
		return out, err
	}
	return s.withOperatorAuth(out, *granted), nil
}

// submit rewrites only params[0]; the remaining elements, including the
// sixth one (AsicBoost version bits), are kept as raw bytes.
func (s *Session) submit(line []byte, msg *stratum.Message) ([]byte, error) {
	params, err := stratum.ParamsArray(msg.Params)
	if err != nil {
		return line, nil
	}
	login, ok := stratum.StringAt(params, 0)
	if ok && len(login) > maxLoginLen {
		return nil, errLoginTooLong
	}
	now := time.Now()

	var a authz
	if s.gateActive() {
		// Device control: a share is forwarded only for a login the Gate
		// allowed, and only while the device may still mine.
		var allowed bool
		s.mu.Lock()
		a, allowed = s.authorized[login]
		s.mu.Unlock()
		if !ok || !allowed {
			writeLine(s.miner, reply(msg.ID, nil, "Unauthorized worker"))
			return nil, nil
		}
		if good, why := s.srv.Gate.Usable(a.deviceID); !good {
			writeLine(s.miner, reply(msg.ID, nil, "Unauthorized worker"))
			return nil, &deniedError{code: why, reason: "device may no longer mine"}
		}
	}

	// The operator's share: a share inside the operator's window is sent with
	// the operator's worker, all other shares with the customer's.
	route := s.operatorRoute(a, now)
	track := a.deviceID != "" && s.operatorReady()

	s.mu.Lock()
	s.lastSubmit = now
	user := login
	if ok {
		var known bool
		if user, known = s.logins[login]; !known {
			user = s.rewrite(login, "", stratum.Vars{}).User
			if len(s.logins) < maxTrackedLogin {
				s.logins[login] = user
			}
		}
	}
	if route.Operator {
		user = s.up.OperatorUser
	}
	if key := msg.IDKey(); key != "" {
		s.sweepLocked(now)
		if len(s.pendingSubmit) < maxPendingIDs {
			worker := s.worker
			if ok {
				worker = login
			}
			s.pendingSubmit[key] = submitInfo{
				at: now, diff: s.difficulty, worker: worker,
				track: track, operator: route.Operator, window: route.Window, deviceID: a.deviceID, customerID: a.customerID,
			}
		}
	}
	s.mu.Unlock()

	if !ok || (user == login && !route.Operator) {
		return line, nil
	}
	params[0] = stratum.EncodeString(user)
	return stratum.ReplaceParams(line, params)
}

// sweepLocked drops submits without an answer for more than a minute.
func (s *Session) sweepLocked(now time.Time) {
	if now.Sub(s.lastSweep) < sweepInterval {
		return
	}
	s.lastSweep = now
	for k, v := range s.pendingSubmit {
		if now.Sub(v.at) > pendingMaxAge {
			delete(s.pendingSubmit, k)
		}
	}
	for k, v := range s.pendingAuth {
		if now.Sub(v.at) > pendingMaxAge {
			delete(s.pendingAuth, k)
		}
	}
}

// fromPool returns the bytes to send to the ASIC for one pool message.
func (s *Session) fromPool(line []byte) ([]byte, error) {
	msg, err := stratum.Parse(line)
	if err != nil {
		s.srv.Stats.UpstreamInvalidJSON()
		return line, nil
	}
	if msg.Method != "" {
		switch msg.Method {
		case "mining.set_difficulty":
			if params, err := stratum.ParamsArray(msg.Params); err == nil {
				if d, ok := stratum.NumberAt(params, 0); ok && d > 0 && d <= maxDifficulty {
					s.mu.Lock()
					s.difficulty = d
					s.mu.Unlock()
				}
			}
		case "mining.notify":
			if stratum.IsJob(msg) {
				s.deadlineMu.Lock()
				s.lastJob = time.Now()
				s.deadlineMu.Unlock()
			}
		case "client.reconnect":
			s.srv.Events.Throttled("reconnect:"+s.up.PoolID, time.Minute, "pool_reconnect",
				"%s sent client.reconnect %s; not forwarded, closing the session so the ASIC comes back to the proxy",
				s.up.PoolName, compact(msg.Params))
			return nil, errPoolReconnect
		}
		return line, nil
	}

	key := msg.IDKey()
	if key == "" {
		return line, nil
	}
	if key == opAuthKey {
		s.operatorAuthAnswer(msg)
		return nil, nil // the ASIC never asked for it
	}
	now := time.Now()
	s.mu.Lock()
	if sub, ok := s.pendingSubmit[key]; ok {
		delete(s.pendingSubmit, key)
		accepted := stratum.ResultTrue(msg.Result) && stratum.IsNull(msg.Error)
		reason := ""
		if accepted {
			s.accepted++
			s.lastShare = now
			s.rate.Add(sub.diff, now)
		} else {
			s.rejected++
			reason = stratum.ErrorReason(msg.Error)
		}
		s.mu.Unlock()
		s.srv.Stats.Share(s.up.PoolID, sub.worker, accepted, sub.diff, reason, now)
		s.noteShare(sub, accepted, reason, now)
		return line, nil
	}
	auth, isAuth := s.pendingAuth[key]
	if isAuth {
		delete(s.pendingAuth, key)
	}
	s.mu.Unlock()
	if isAuth && (!stratum.ResultTrue(msg.Result) || !stratum.IsNull(msg.Error)) {
		reason := stratum.ErrorReason(msg.Error)
		if reason == "" {
			reason = "result " + compact(msg.Result)
		}
		s.srv.Events.Throttled("auth:"+s.up.PoolID+":"+auth.user, time.Minute, "authorization_failed",
			"authorization failed on %s for %q: %s", s.up.PoolName, auth.user, reason)
		return line, &upstreamError{kind: AuthorizationRejected, err: fmt.Errorf("mining.authorize as %q: %s", auth.user, reason)}
	}
	if isAuth {
		s.deadlineMu.Lock()
		if s.authorizedAt.IsZero() {
			s.authorizedAt = now
		}
		s.deadlineMu.Unlock()
	}
	return line, nil
}

func compact(raw json.RawMessage) string {
	if len(raw) > 200 {
		return string(raw[:200]) + "…"
	}
	return string(raw)
}
