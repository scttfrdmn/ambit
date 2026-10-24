package main

import (
	"context"
	"encoding/json"
	"flag"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/bedrock"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/lki/quill"
	"github.com/scttfrdmn/ambit/internal/report"
	"github.com/scttfrdmn/ambit/internal/run"
	"github.com/scttfrdmn/ambit/internal/swarm"
)

const testCorpus = "../../corpus"

// fakeModels serves every model API the arms reach: llama-server's
// /tokenize and /completion (Jebadiah), strands-decider's /health and
// /v1/systemone (strands and d3), and the Decisions API (hosted).
func fakeModels(t *testing.T) *httptest.Server { return slowFakeModels(t, 0) }

// slowFakeModels is fakeModels with every reply delayed by delay.
func slowFakeModels(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	const boundary = "</think>\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		var req map[string]any
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
		}
		var out any
		switch r.URL.Path {
		case "/tokenize":
			// The prompt is one token; each label after the answer boundary
			// is one more.
			content, _ := req["content"].(string)
			toks := []int{1}
			if i := strings.LastIndex(content, boundary); i >= 0 && i+len(boundary) < len(content) {
				toks = append(toks, 1000+int(content[i+len(boundary)]))
			}
			out = map[string]any{"tokens": toks}
		case "/completion":
			// Label A (true) a little likelier than B (false).
			out = map[string]any{"completion_probabilities": []any{map[string]any{"top_logprobs": []any{
				map[string]any{"id": 1000 + 'A', "logprob": -0.4}, map[string]any{"id": 1000 + 'B', "logprob": -1.1}}}}}
		case "/health":
			out = map[string]any{"status": "ok", "model": "fake", "checkpoint": "fake", "device": "cpu"}
		case "/v1/systemone":
			qs, _ := req["questions"].(map[string]any)
			ans := map[string]any{}
			for id := range qs {
				ans[id] = map[string]any{"type": "noul", "noul": 0.7}
			}
			out = map[string]any{"model": "fake", "answers": ans, "usage": map[string]int{"input_tokens": 900, "output_tokens": 1}, "latency_ms": 1.0}
		case "/v1/decisions":
			out = map[string]any{"answers": []any{map[string]any{"type": "predicate", "name": "q", "probability": 0.7}},
				"model": "gpt-6-luna-fake", "usage": map[string]any{"input_tokens": 1200}}
		default:
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeSafeguard answers ALLOWED as a gate and NO as a PIP.
type fakeSafeguard struct{}

func (fakeSafeguard) Ask(_ context.Context, system, _ string) (bedrock.Answer, error) {
	a := bedrock.Answer{Word: "NO", FinishReason: "stop", ServedModel: "fake-safeguard", Date: "2026-10-24", InputTokens: 1000, CostMicros: 75}
	if system == arms.SafeguardGateSystem {
		a.Word = "ALLOWED"
	}
	return a, nil
}

// useFakes points every arm at the fakes.
func useFakes(t *testing.T) []string {
	t.Helper()
	srv := fakeModels(t)
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	old := newSafeguard
	newSafeguard = func(context.Context, string) (arms.SafeguardAsker, error) { return fakeSafeguard{}, nil }
	t.Cleanup(func() { newSafeguard = old })
	return []string{"-llama", srv.URL, "-strands", srv.URL, "-d3", srv.URL}
}

var everyArm = []string{"b0", "b", "b-4b", "b-hosted", "b-strands", "b-safeguard", "a-local", "a-hosted", "a-strands", "a-d3", "a-safeguard"}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()
	var v T
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func onlyRun(t *testing.T, results string) string {
	t.Helper()
	ms, _ := filepath.Glob(filepath.Join(results, "*", "manifest.json"))
	if len(ms) != 1 {
		t.Fatalf("%d runs under %s", len(ms), results)
	}
	return filepath.Dir(ms[0])
}

// TestSwarmEveryArm: every arm ambit run accepts gates a swarm, built by the
// same code with the same configuration (Law 3); the Cedar arms write a
// quill entry per decision and the A arms none.
func TestSwarmEveryArm(t *testing.T) {
	fakes := useFakes(t)
	for _, arm := range everyArm {
		t.Run(arm, func(t *testing.T) {
			base := t.TempDir()
			rres, sres := filepath.Join(base, "run"), filepath.Join(base, "swarm")
			args := append([]string{"-arm", arm, "-dir", testCorpus}, fakes...)
			if err := cmdRun(append(args, "-results", rres, "-limit", "3")); err != nil {
				t.Fatalf("ambit run: %v", err)
			}
			if err := cmdSwarm(append(args, "-results", sres, "-agents", "10", "-server-slots", "4")); err != nil {
				t.Fatalf("ambit swarm: %v", err)
			}
			rm := readJSON[run.Manifest](t, filepath.Join(onlyRun(t, rres), "manifest.json"))
			sdir := onlyRun(t, sres)
			sm := readJSON[swarm.Manifest](t, filepath.Join(sdir, "manifest.json"))
			if sm.Arm != arm || rm.Arm != arm || sm.Kind != swarm.ManifestKind || sm.Finished == "" || sm.Stopped != "" {
				t.Fatalf("swarm manifest %+v", sm)
			}
			// strands_server_reports and the like are the same server's.
			if !maps.Equal(sm.Models, rm.Models) || sm.ProseSHA256 != rm.ProseSHA256 || sm.CachePrompt != rm.CachePrompt {
				t.Errorf("gate configuration differs:\n run   %v %s\n swarm %v %s", rm.Models, rm.ProseSHA256, sm.Models, sm.ProseSHA256)
			}
			if (arm[0] == 'a') != (sm.ProseSHA256 != "") {
				t.Errorf("prose hash %q for %s", sm.ProseSHA256, arm)
			}
			if sm.Errors != 0 || sm.Gated == 0 || sm.ServerSlots != 4 || sm.RunCapMicros != 5_000_000 || rm.RunCapMicros != 5_000_000 {
				t.Errorf("errors %d, gated %d, slots %d, run cap %d / %d", sm.Errors, sm.Gated, sm.ServerSlots, sm.RunCapMicros, rm.RunCapMicros)
			}
			calls, err := corpus.ReadJSONL[swarm.Call](filepath.Join(sdir, "calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			es, _ := quill.ReadAll(filepath.Join(sdir, "quill.jsonl"))
			want := 0
			if arm[0] == 'b' {
				want = len(calls)
			}
			if len(es) != want || sm.QuillEntries != want {
				t.Errorf("%d quill entries (manifest %d) for %d calls, want %d", len(es), sm.QuillEntries, len(calls), want)
			}
			if arm == "a-hosted" || arm == "b-hosted" {
				if sm.SpendMicros <= 0 {
					t.Errorf("hosted spend %d", sm.SpendMicros)
				}
			}
			if strings.HasSuffix(arm, "safeguard") && sm.ModelCostMicros <= 0 {
				t.Errorf("safeguard model cost %d", sm.ModelCostMicros)
			}
		})
	}
	// The report renders over a swarm results directory.
	res := t.TempDir()
	if err := cmdSwarm(append([]string{"-arm", "b0", "-dir", testCorpus, "-results", res, "-agents", "10"}, fakes...)); err != nil {
		t.Fatal(err)
	}
	if err := cmdSwarmReport([]string{"-results", res, "-resamples", "50"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(res, "swarm-report.md")); err != nil {
		t.Fatal(err)
	}
}

// TestSwarmHostedCap: the swarm respects the hosted per-run cap as ambit
// run does: a spend-cap error halts it and the run is marked stopped.
func TestSwarmHostedCap(t *testing.T) {
	fakes := useFakes(t)
	res := t.TempDir()
	err := cmdSwarm(append([]string{"-arm", "a-hosted", "-dir", testCorpus, "-results", res, "-agents", "10", "-run-cap", "0.0001"}, fakes...))
	if err == nil || !strings.Contains(err.Error(), "per-run cap") {
		t.Fatalf("err = %v, want a per-run cap stop", err)
	}
	m := readJSON[swarm.Manifest](t, filepath.Join(onlyRun(t, res), "manifest.json"))
	if !strings.Contains(m.Stopped, "per-run cap") || m.RunCapMicros != 100 || m.SpendMicros > 100 {
		t.Errorf("manifest stopped %q, cap %d, spend %d", m.Stopped, m.RunCapMicros, m.SpendMicros)
	}
}

// TestRunCapBounds: -run-cap must be above $0 and at most $15.
func TestRunCapBounds(t *testing.T) {
	for _, c := range []struct {
		dollars float64
		micros  int64
		ok      bool
	}{{5, 5_000_000, true}, {15, 15_000_000, true}, {0.5, 500_000, true}, {15.01, 0, false}, {16, 0, false}, {0, 0, false}, {-1, 0, false}} {
		got, err := runCapMicros(c.dollars)
		if (err == nil) != c.ok || got != c.micros {
			t.Errorf("$%v: %d, %v", c.dollars, got, err)
		}
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		gf := addGateFlags(fs)
		if err := fs.Parse([]string{"-run-cap", fmtFloat(c.dollars)}); err != nil {
			t.Fatal(err)
		}
		b, err := gf.build(context.Background(), testCorpus, t.TempDir())
		if (err == nil) != c.ok || c.ok && b.runCapMicros != c.micros {
			t.Errorf("build with -run-cap %v: %v", c.dollars, err)
		}
	}
	if err := cmdRun([]string{"-arm", "b0", "-dir", testCorpus, "-results", t.TempDir(), "-run-cap", "20"}); err == nil {
		t.Error("ambit run accepted -run-cap 20")
	}
	if err := cmdSwarm([]string{"-arm", "b0", "-dir", testCorpus, "-results", t.TempDir(), "-run-cap", "20"}); err == nil {
		t.Error("ambit swarm accepted -run-cap 20")
	}
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// TestSwarmSIGTERM: a swarm stopped by SIGTERM (a spore TTL stop) mid-run
// keeps the calls it streamed under a stopped, unfinished manifest, and the
// report lists it as incomplete and scores nothing from it.
func TestSwarmSIGTERM(t *testing.T) {
	srv := slowFakeModels(t, 5*time.Millisecond)
	res := t.TempDir()
	var done atomic.Bool
	errc := make(chan error, 1)
	go func() {
		err := cmdSwarm([]string{"-arm", "a-d3", "-d3", srv.URL, "-dir", testCorpus, "-results", res, "-agents", "100", "-concurrency", "2"})
		done.Store(true)
		errc <- err
	}()
	// The manifest is written after the signal handler is installed; the
	// first calls reach calls.jsonl within a second.
	deadline := time.Now().Add(20 * time.Second)
	for {
		ms, _ := filepath.Glob(filepath.Join(res, "*", "calls.jsonl"))
		if len(ms) == 1 {
			if fi, err := os.Stat(ms[0]); err == nil && fi.Size() > 0 {
				break
			}
		}
		if done.Load() || time.Now().After(deadline) {
			t.Fatal("the swarm ended, or streamed nothing, before it could be signalled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if done.Load() {
		t.Fatal("the swarm finished before it could be signalled")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want an interrupted stop", err)
	}
	dir := onlyRun(t, res)
	m := readJSON[swarm.Manifest](t, filepath.Join(dir, "manifest.json"))
	if m.Finished != "" || m.Stopped != "interrupted" || m.Tasks == 0 || m.AgentsRan == 0 {
		t.Fatalf("manifest finished %q, stopped %q, tasks %d, agents ran %d", m.Finished, m.Stopped, m.Tasks, m.AgentsRan)
	}
	calls, err := corpus.ReadJSONL[swarm.Call](filepath.Join(dir, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 || len(calls) != m.Gated || len(calls) >= m.ScriptedCalls {
		t.Fatalf("%d calls streamed, manifest gated %d of %d scripted", len(calls), m.Gated, m.ScriptedCalls)
	}
	runs, err := report.LoadSwarm(res)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !runs[0].Incomplete() || runs[0].RecordedCalls != len(calls) {
		t.Fatalf("loaded %d runs", len(runs))
	}
	md, data := report.BuildSwarm(runs, report.Options{Resamples: 20})
	if inc := data["incomplete"].([]map[string]any); len(inc) != 1 || inc[0]["calls_recorded"] != len(calls) {
		t.Errorf("incomplete listing %v", inc)
	}
	if len(data["gate_latency"].([]map[string]any)) != 0 || !strings.Contains(md, "## Incomplete runs (no verdict)") {
		t.Errorf("an incomplete run was scored:\n%s", md)
	}
}
