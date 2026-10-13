package pool

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

func callWithResult(t *testing.T, result string) error {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		r := bufio.NewReader(server)
		if _, err := r.ReadBytes('\n'); err == nil {
			_, _ = fmt.Fprintf(server, `{"id":1,"result":%s,"error":null}`+"\n", result)
		}
	}()
	return call(client, bufio.NewReader(client), 1, "mining.subscribe", []any{"test"}, false)
}

const testJob = `{"id":null,"method":"mining.notify","params":["job1","prev","c1","c2",[],"20000000","1d00ffff","00000000",true]}`

// Serve real TCP sockets and close every accepted connection on cleanup,
// including checks waiting for a job or a deliberately delayed response.
func checkAddress(t *testing.T, serve func(net.Conn)) state.Address {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	var wg sync.WaitGroup
	acceptedDone := make(chan struct{})
	go func() {
		defer close(acceptedDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			wg.Add(1)
			go func() { defer wg.Done(); defer c.Close(); serve(c) }()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-acceptedDone
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return state.Address{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}
}

func answerCheck(c net.Conn, placement, authResult, authError string) {
	lr := stratum.NewLineReader(c, 65536)
	for {
		line, err := lr.ReadLine()
		if err != nil {
			return
		}
		msg, err := stratum.Parse(line)
		if err != nil {
			return
		}
		switch msg.Method {
		case "mining.subscribe":
			if placement == "before_subscribe" {
				fmt.Fprintln(c, testJob)
			}
			fmt.Fprintf(c, `{"id":%s,"result":[[],"extra",4],"error":null}`+"\n", msg.ID)
		case "mining.authorize":
			if placement == "before_authorize" {
				fmt.Fprintln(c, testJob)
			}
			fmt.Fprintf(c, `{"id":%s,"result":%s,"error":%s}`+"\n", msg.ID, authResult, authError)
			if placement == "after_authorize" {
				fmt.Fprintln(c, testJob)
			}
		}
	}
}

func TestCheckRequiresAuthorizationAndUsableJob(t *testing.T) {
	for _, tc := range []struct {
		name, placement, result, poolError, wantError string
	}{
		{"early subscribe job", "before_subscribe", "true", "null", ""},
		{"early authorize job", "before_authorize", "true", "null", ""},
		{"late job", "after_authorize", "true", "null", ""},
		{"silent authorized pool", "none", "true", "null", "mining.notify"},
		{"banned even with true result and early job", "before_authorize", "true", `[24,"miner still banned",null]`, "miner still banned"},
		{"false authorization", "before_authorize", "false", "null", "rejected by pool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := checkAddress(t, func(c net.Conn) { answerCheck(c, tc.placement, tc.result, tc.poolError) })
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			rtt, err := Check(ctx, state.Pool{Username: "account.{worker}"}, a, true, "proxytest")
			if rtt <= 0 {
				t.Fatal("successful connect must retain RTT even when authorization/jobs fail")
			}
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("check error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestSubscribeCheckRequiresValidResult(t *testing.T) {
	for _, result := range []string{"false", `{}`, `[]`, `[[],"extra"]`} {
		if err := callWithResult(t, result); err == nil {
			t.Errorf("result %s was accepted", result)
		}
	}
	if err := callWithResult(t, `[[["mining.notify","id"]],"extranonce",4]`); err != nil {
		t.Fatalf("valid subscribe result rejected: %v", err)
	}
}

func TestCallAcceptsStringEchoedID(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		r := bufio.NewReader(server)
		if _, err := r.ReadBytes('\n'); err == nil {
			_, _ = fmt.Fprint(server, `{"id":"1","result":true,"error":null}`+"\n")
		}
	}()
	if err := call(client, bufio.NewReader(client), 1, "mining.authorize", []any{"u", "x"}, true); err != nil {
		t.Fatalf("string id not matched: %v", err)
	}
}
