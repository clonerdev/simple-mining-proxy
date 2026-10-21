// Command proxy is the SHA256 Stratum V1 proxy with an embedded admin UI.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/accounts"
	"github.com/famfamfam/simple-mining-proxy/internal/admin"
	"github.com/famfamfam/simple-mining-proxy/internal/audit"
	"github.com/famfamfam/simple-mining-proxy/internal/config"
	"github.com/famfamfam/simple-mining-proxy/internal/devices"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/history"
	"github.com/famfamfam/simple-mining-proxy/internal/listener"
	"github.com/famfamfam/simple-mining-proxy/internal/monitor"
	"github.com/famfamfam/simple-mining-proxy/internal/opshare"
	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/profit"
	"github.com/famfamfam/simple-mining-proxy/internal/rtt"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
	"github.com/famfamfam/simple-mining-proxy/internal/telegram"
	"github.com/famfamfam/simple-mining-proxy/internal/timed"
	"github.com/famfamfam/simple-mining-proxy/internal/tlsutil"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// healthcheck is the Docker healthcheck: distroless has no curl.
func healthcheck() int {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(config.AdminHealthURL())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "status", resp.Status)
		return 1
	}
	return 0
}

func parseLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return l
}

func run() error {
	started := time.Now()
	level := new(slog.LevelVar)
	opts := &slog.HandlerOptions{Level: level}
	format := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT")))
	if format == "text" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, opts)))
	} else {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, opts)))
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	st, err := state.Open(filepath.Join(cfg.DataDir, "state.json"))
	if err != nil {
		return err
	}
	set, unknown, err := settings.NewStore(st.Current().Settings)
	if err != nil {
		return err
	}
	for _, k := range unknown {
		slog.Warn("unknown setting in state.json is ignored", "key", k)
	}
	level.Set(parseLevel(set.Get().LogLevel))
	set.OnChange(func(v *settings.Values) { level.Set(parseLevel(v.LogLevel)) })

	// Block hunting adds a few events per eCash block (every ~10 minutes):
	// room for about two days of them next to everything else.
	ev := events.New(2000)
	reg := session.NewRegistry(ev)
	set.OnChange(reg.RefreshIdleDeadlines)
	collector := stats.NewCollector()
	mgr := pool.NewManager(st, set, reg, ev)

	srv := &session.Server{Settings: set, Router: mgr, Registry: reg, Events: ev, Stats: collector}

	// Accounts and the audit log live next to state.json. A missing accounts
	// file is an empty one: the administrator from the environment always
	// works, so an install that never creates an account behaves as before.
	acc, err := accounts.Open(filepath.Join(cfg.DataDir, "accounts.json"), cfg.AdminUsername)
	if err != nil {
		return fmt.Errorf("accounts: %w", err)
	}
	aud, err := audit.Open(filepath.Join(cfg.DataDir, "audit"))
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	aud.Prune(set.Get().AuditRetention)
	set.OnChange(func(v *settings.Values) { go aud.Prune(v.AuditRetention) })

	// Customers and their devices. The gate is consulted by every Stratum
	// session; it lets everybody in until "device_control" is switched on in
	// Settings, which is how the proxy behaved before device control existed.
	dev, err := devices.Open(filepath.Join(cfg.DataDir, "devices.json"))
	if err != nil {
		return fmt.Errorf("devices: %w", err)
	}
	enforce := &devices.Enforcement{Store: dev, Registry: reg, Audit: aud, Events: ev}
	enforce.Install()
	srv.Gate = devices.NewGate(devices.GateConfig{
		Store:        dev,
		Enabled:      func() bool { return set.Get().DeviceControlEnabled() },
		PendingLimit: func() int { return set.Get().DevicePendingLimit },
		Live:         reg.LiveCustomer,
	})

	// The operator's optional share of a customer's mining. It does nothing
	// until "operator_share" is switched on, a pool is marked as crediting a
	// second worker, and a customer has recorded consent; the session checks
	// all three on every share. It needs device control: without a customer
	// there is nobody who could have consented.
	opShare := opshare.NewRouter(filepath.Join(cfg.DataDir, "opshare"),
		func() opshare.Config {
			v := set.Get()
			return opshare.Config{
				Enabled:     v.OperatorShare == settings.OperatorShareOn,
				Cycle:       v.OperatorShareCycle,
				MaxFraction: float64(v.OperatorShareMaxPct) / 100,
			}
		},
		// A device's own size comes first, then its customer's, then the setting.
		opshare.ScopedRules{
			Base:      opshare.FlatRules{Fraction: func() float64 { return set.Get().OperatorShareFraction() }},
			Overrides: dev,
		},
		dev)
	opShare.OnTrip = func(poolID, reason string) {
		ev.Warn("operator_share_off", "operator share switched off on pool %s for a while: %s", poolID, reason)
	}
	srv.Splitter = opShare

	hist, err := history.New(filepath.Join(cfg.DataDir, "history"),
		func() history.Sample {
			return history.Sample{Totals: collector.Totals(), Miners: reg.Counts().ByPool, Online: reg.Workers()}
		},
		func() history.Retention {
			v := set.Get()
			return history.Retention{Detail: v.HistoryDetail, Total: v.HistoryRetention, Miners: v.HistoryMiners}
		})
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	set.OnChange(func(*settings.Values) { go hist.Prune() })

	// Market data requests are bounded by the callers' contexts.
	httpClient := &http.Client{}
	// eCash blocks right after a block must meet a harder real-time target:
	// a quiet connection to an eCash pool shows when blocks arrive. A block
	// wakes the timer at once, so block hunting leaves without delay.
	var timedSw *timed.Switcher
	watcher := rtt.NewWatcher(rtt.WatchDeps{
		Settings: set, Pools: mgr, Seed: rtt.Blockchair(httpClient, rtt.BlockchairURL),
		OnBlock: func() { timedSw.Kick() },
	})
	timedSw = timed.New(timed.Deps{
		Settings: set, Pools: mgr, Events: ev, Path: filepath.Join(cfg.DataDir, "timed.json"),
		Hardness: func(p state.Pool) float64 {
			if p.Coin == rtt.Coin {
				return watcher.Hardness()
			}
			return 1
		},
		Live: func(p state.Pool) bool { return p.Coin == rtt.Coin && watcher.Live() },
	})
	profitSw := profit.New(profit.Deps{
		Settings: set, Pools: mgr, Events: ev, Path: filepath.Join(cfg.DataDir, "profit.json"),
		Fetch: profit.WithBSV(httpClient, profit.WhatsOnChainURL, profit.WhatToMine(httpClient, profit.WhatToMineURL)),
		Home:  timedSw.Home,
	})

	// ASICs that stop sending shares go to the events, and to Telegram once
	// a bot token is set in the admin UI.
	var mon *monitor.Monitor
	// TELEGRAM_BOT_TOKEN wins; a token saved in state.json by an earlier
	// version keeps working until it is moved to the environment.
	botToken, tokenFromEnv := st.Current().TelegramToken, false
	if cfg.TelegramBotToken != "" {
		if !telegram.ValidToken(cfg.TelegramBotToken) {
			return errors.New("TELEGRAM_BOT_TOKEN does not look like a bot token (123456789:AA… from @BotFather)")
		}
		botToken, tokenFromEnv = cfg.TelegramBotToken, true
	} else if botToken != "" {
		ev.Warn("telegram_token_stored", "the Telegram bot token is stored in state.json: move it to TELEGRAM_BOT_TOKEN in .env, then remove it in Settings")
	}
	bot := telegram.New(telegram.Deps{
		Token: botToken, TokenFromEnv: tokenFromEnv, APIURL: cfg.TelegramAPIURL, Client: httpClient, Settings: set, Events: ev,
		Miners: func() []monitor.Miner { return mon.Miners() },
		Farm:   farmSummary(mgr, collector, timedSw, started),
	})
	mon = monitor.New(monitor.Deps{
		Settings: set, Miners: reg.Infos, Events: ev, Path: filepath.Join(cfg.DataDir, "monitor.json"), Notify: bot.Notify,
	})

	var certs *tlsutil.Certs
	if cfg.StratumTLSAddr != "" {
		var generated bool
		certs, generated, err = tlsutil.Load(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSSelfSignedHosts)
		if err != nil {
			return err
		}
		info := certs.Info()
		if generated {
			ev.Warn("tls_self_signed", "generated a self-signed TLS certificate for %v, SHA-256 %s", info.DNSNames, info.SHA256)
		}
		base := &tls.Config{GetCertificate: certs.GetCertificate}
		// A fresh config per connection picks up tls_min_version without a restart.
		srv.TLSConfig = func() *tls.Config {
			c := base.Clone()
			c.MinVersion = tlsutil.MinVersion(set.Get().TLSMinVersion)
			return c
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	limits := listener.NewLimits(set, ev)
	var listeners []*listener.Listener
	if cfg.StratumTCPAddr != "" {
		listeners = append(listeners, &listener.Listener{Name: "tcp", Addr: cfg.StratumTCPAddr, Server: srv, Limits: limits})
	}
	if cfg.StratumTLSAddr != "" {
		listeners = append(listeners, &listener.Listener{Name: "tls", Addr: cfg.StratumTLSAddr, TLS: true, Server: srv, Limits: limits})
	}
	for _, l := range listeners {
		if err := l.Listen(ctx); err != nil {
			return err
		}
	}

	adminLn, err := net.Listen("tcp", cfg.AdminAddr)
	if err != nil {
		return fmt.Errorf("admin listener on %s: %w", cfg.AdminAddr, err)
	}
	httpSrv := &http.Server{
		Handler: admin.New(admin.Deps{
			Config: cfg, State: st, Settings: set, Pools: mgr, Registry: reg,
			Stats: collector, History: hist, Profit: profitSw, Timed: timedSw, RTT: watcher, Telegram: bot, Accounts: acc, Audit: aud, Devices: dev, OperatorShare: opShare,
			Events: ev, Certs: certs, Started: started,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	slog.Info("starting", "config", cfg)
	snap := mgr.Snapshot()
	if len(snap.Pools) == 0 {
		ev.Warn("no_pools", "no pools configured yet: add one in the admin UI")
	}
	ev.Info("started", "proxy started: %d pools, active %q", len(snap.Pools), snap.Active)

	var sessions sync.WaitGroup
	var acceptors sync.WaitGroup
	for _, l := range listeners {
		acceptors.Add(1)
		go func(l *listener.Listener) {
			defer acceptors.Done()
			l.Serve(ctx, &sessions)
		}(l)
	}
	go limits.ReportRejected(ctx)
	go mgr.Run(ctx)
	histDone := make(chan struct{})
	go func() { hist.Run(ctx); close(histDone) }()
	go func() { // the audit log keeps what audit_retention says, checked daily
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				aud.Prune(set.Get().AuditRetention)
			}
		}
	}()
	go profitSw.Run(ctx)
	go timedSw.Run(ctx)
	go watcher.Run(ctx)
	monDone := make(chan struct{})
	go func() { mon.Run(ctx); close(monDone) }()
	go bot.Run(ctx)
	devDone := make(chan struct{})
	go func() { dev.Run(ctx); close(devDone) }()
	go enforce.Run(ctx)
	opDone := make(chan struct{})
	go func() { opShare.Run(ctx); close(opDone) }()
	httpErr := make(chan error, 1)
	go func() {
		slog.Info("admin UI listening", "addr", adminLn.Addr().String())
		httpErr <- httpSrv.Serve(adminLn)
	}()

	select {
	case <-ctx.Done():
	case err := <-httpErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("admin server: %w", err)
		}
	}

	// Shutdown: stop accepting and wait until accept loops can no longer add
	// sessions, then close all sessions and stop HTTP.
	slog.Info("shutting down")
	stop()
	acceptors.Wait()
	reg.CloseAll("shutdown")
	shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	httpSrv.Shutdown(shCtx)
	done := make(chan struct{})
	go func() { sessions.Wait(); close(done) }()
	select {
	case <-done:
	case <-shCtx.Done():
		slog.Warn("some sessions did not finish in time")
	}
	// The history writes the unfinished minute and the monitor the last
	// shares when ctx is done.
	for _, done := range []chan struct{}{histDone, monDone, devDone, opDone} {
		select {
		case <-done:
		case <-shCtx.Done():
		}
	}
	return nil
}

// farmSummary is the part of the Telegram /status that the monitor does not
// know.
func farmSummary(mgr *pool.Manager, collector *stats.Collector, timedSw *timed.Switcher, started time.Time) func() telegram.Farm {
	return func() telegram.Farm {
		now := time.Now()
		snap := mgr.Snapshot()
		st := collector.Snapshot(now)
		f := telegram.Farm{
			Hashrate: st.HashrateHs, Mode: mgr.Mode(),
			Accepted: st.Accepted, Rejected: st.Rejected, Uptime: now.Sub(started),
		}
		if p, ok := snap.Get(mgr.Effective()); ok {
			f.Pool, f.Coin, f.Solo = p.Name, p.Coin, p.Solo
		}
		ts := timedSw.Status()
		if ts.Home != "" && ts.Until != nil {
			if p, ok := snap.Get(ts.Target); ok {
				f.Timer = &telegram.Timer{Pool: p.Name, Left: ts.Until.Sub(now)}
			}
		}
		if h := ts.Hunt; h.Mode == settings.HuntOn && h.Target != "" {
			if p, ok := snap.Get(h.Target); ok {
				f.Hunt = &telegram.Hunt{Pool: p.Name, Hardness: h.Hardness, Live: h.Live,
					Stints: h.Stints, Time: time.Duration(h.Seconds * float64(time.Second))}
				if h.Since != nil {
					f.Hunt.For = now.Sub(*h.Since)
				}
			}
		}
		return f
	}
}
