package hosted

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fake(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/decisions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("bad request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Model != Model || len(req.Questions) != 1 || req.Questions[0].Type != "predicate" {
			t.Errorf("bad body %+v", req)
		}
		w.Write([]byte(`{"answers":[{"type":"predicate","name":"q","probability":0.25}],"model":"gpt-6-luna-2026-10-06","usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":1200}}`))
	}))
}

func TestAskAndLedger(t *testing.T) {
	srv := fake(t)
	defer srv.Close()
	log := filepath.Join(t.TempDir(), "spend.jsonl")
	l, err := OpenLedger(log, "run1", 5_000_000, 200_000_000)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{BaseURL: srv.URL, Key: "k", HTTP: srv.Client(), Ledger: l}
	a, err := c.Ask(context.Background(), "STATE", "It is allowed.")
	if err != nil {
		t.Fatal(err)
	}
	if a.P != 0.25 || a.ServedModel != "gpt-6-luna-2026-10-06" || a.InputTokens != 1200 || a.CostMicros != 120 {
		t.Fatalf("answer %+v", a)
	}
	if err := l.Log(a, "r0001"); err != nil {
		t.Fatal(err)
	}
	// A second ledger on the same log is refused while the first is open.
	if _, err := OpenLedger(log, "run1b", 5_000_000, 200_000_000); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("want a concurrent-ledger refusal, got %v", err)
	}
	l.Close()
	// The log carries into the next run's project total.
	l2, err := OpenLedger(log, "run2", 5_000_000, 200)
	if err != nil {
		t.Fatal(err)
	}
	c.Ledger = l2
	if _, err := c.Ask(context.Background(), strings.Repeat("x", 3000), "q"); err == nil || !strings.Contains(err.Error(), "project ceiling") {
		t.Fatalf("want project ceiling refusal, got %v", err)
	}
	// Per-run cap.
	l3, _ := OpenLedger(filepath.Join(t.TempDir(), "s.jsonl"), "run3", 10, 200_000_000)
	c.Ledger = l3
	if _, err := c.Ask(context.Background(), strings.Repeat("x", 3000), "q"); err == nil || !strings.Contains(err.Error(), "per-run cap") {
		t.Fatalf("want per-run cap refusal, got %v", err)
	}
}

// failing serves the given status and body for the first n calls, then the
// fake's answer, and counts calls.
func failing(t *testing.T, n int, status int, body string, calls *atomic.Int32) *httptest.Server {
	ok := fake(t)
	t.Cleanup(ok.Close)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(calls.Add(1)) <= n {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(status)
			w.Write([]byte(body))
			return
		}
		ok.Config.Handler.ServeHTTP(w, r)
	}))
}

func TestRetryRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := failing(t, 2, http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`, &calls)
	defer srv.Close()
	l, _ := OpenLedger(filepath.Join(t.TempDir(), "s.jsonl"), "run", 5_000_000, 200_000_000)
	c := &Client{BaseURL: srv.URL, Key: "k", HTTP: srv.Client(), Ledger: l, Backoff: time.Millisecond}
	a, err := c.Ask(context.Background(), "STATE", "It is allowed.")
	if err != nil {
		t.Fatal(err)
	}
	if a.Attempts != 3 || calls.Load() != 3 || a.P != 0.25 {
		t.Fatalf("attempts %d, calls %d, answer %+v", a.Attempts, calls.Load(), a)
	}
	if l.RunSpent() != 120 || l.pending != 0 {
		t.Fatalf("spent %d, pending %d", l.RunSpent(), l.pending)
	}
}

func TestNoRetryOnQuota(t *testing.T) {
	var calls atomic.Int32
	srv := failing(t, 100, http.StatusTooManyRequests, `{"error":{"code":"insufficient_quota","message":"no credit"}}`, &calls)
	defer srv.Close()
	l, _ := OpenLedger(filepath.Join(t.TempDir(), "s.jsonl"), "run", 5_000_000, 200_000_000)
	c := &Client{BaseURL: srv.URL, Key: "k", HTTP: srv.Client(), Ledger: l, Backoff: time.Millisecond}
	a, err := c.Ask(context.Background(), "STATE", "It is allowed.")
	if err == nil || !strings.Contains(err.Error(), "insufficient_quota") {
		t.Fatalf("want quota error, got %v", err)
	}
	if a.Attempts != 1 || calls.Load() != 1 || l.pending != 0 {
		t.Fatalf("attempts %d, calls %d, pending %d", a.Attempts, calls.Load(), l.pending)
	}
}

func TestRetryGivesUp(t *testing.T) {
	var calls atomic.Int32
	srv := failing(t, 100, http.StatusServiceUnavailable, `overloaded`, &calls)
	defer srv.Close()
	l, _ := OpenLedger(filepath.Join(t.TempDir(), "s.jsonl"), "run", 5_000_000, 200_000_000)
	c := &Client{BaseURL: srv.URL, Key: "k", HTTP: srv.Client(), Ledger: l, Backoff: time.Millisecond}
	a, err := c.Ask(context.Background(), "STATE", "It is allowed.")
	if err == nil || a.Attempts != MaxAttempts || int(calls.Load()) != MaxAttempts {
		t.Fatalf("err %v, attempts %d, calls %d", err, a.Attempts, calls.Load())
	}
	if l.pending != 0 || l.RunSpent() != 0 {
		t.Fatalf("pending %d, spent %d", l.pending, l.RunSpent())
	}
}

// TestLedgerConcurrent reserves from many goroutines at once: exactly as
// many calls as the cap allows go ahead, and the cap is never crossed.
func TestLedgerConcurrent(t *testing.T) {
	const est, limit, n = 7, 700, 1000
	l, _ := OpenLedger(filepath.Join(t.TempDir(), "s.jsonl"), "run", limit, 200_000_000)
	var ok atomic.Int32
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Reserve(est) != nil {
				return
			}
			ok.Add(1)
			l.mu.Lock()
			over := l.runSpent+l.pending > limit
			l.mu.Unlock()
			if over {
				t.Error("cap crossed")
			}
			l.Settle(est, est)
		}()
	}
	wg.Wait()
	if ok.Load() != limit/est || l.RunSpent() != limit || l.pending != 0 {
		t.Fatalf("%d reserved, spent %d, pending %d", ok.Load(), l.RunSpent(), l.pending)
	}
	// Releases return headroom.
	l2, _ := OpenLedger(filepath.Join(t.TempDir(), "s.jsonl"), "run", limit, 200_000_000)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l2.Reserve(est) == nil {
				l2.Release(est)
			}
		}()
	}
	wg.Wait()
	if l2.pending != 0 || l2.RunSpent() != 0 {
		t.Fatalf("pending %d, spent %d", l2.pending, l2.RunSpent())
	}
}
