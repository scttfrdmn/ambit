package hosted

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
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
