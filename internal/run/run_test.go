package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/lki/pdp"
)

const n = 40

// testCorpus writes a frozen n-record sharded corpus with the real policies.
func testCorpus(t *testing.T) string {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, corpus.PolicyDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{corpus.CedarFile, corpus.ProseFile} {
		b, err := os.ReadFile(filepath.Join("..", "..", "corpus", corpus.PolicyDir, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, corpus.PolicyDir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recs := make([]corpus.Record, n)
	truths := make([]corpus.Truth, n)
	for i := range recs {
		id := fmt.Sprintf("r%04d", i)
		recs[i] = corpus.Record{ID: id, Task: "t", Facts: corpus.Facts{Tool: "query_dataset",
			Chain: []corpus.Principal{{ID: "a0", Role: "analyst", ActingFor: "u"}}}}
		truths[i] = corpus.Truth{ID: id, Verdict: corpus.Deny}
	}
	if err := corpus.WriteShards(dir, "records", recs); err != nil {
		t.Fatal(err)
	}
	if err := corpus.WriteShards(dir, "oracle", truths); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.WriteSums(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeGate counts calls per record and the most decided at once. Records
// in stopAt fail with a spend-cap error, and records in errAt with that
// error.
type fakeGate struct {
	mu       sync.Mutex
	calls    map[string]int
	inflight atomic.Int32
	peak     atomic.Int32
	stopAt   map[string]bool
	errAt    map[string]string
}

func (g *fakeGate) Name() string { return "fake" }

func (g *fakeGate) Decide(ctx context.Context, r corpus.Record) arms.Decision {
	cur := g.inflight.Add(1)
	defer g.inflight.Add(-1)
	for p := g.peak.Load(); cur > p && !g.peak.CompareAndSwap(p, cur); p = g.peak.Load() {
	}
	g.mu.Lock()
	g.calls[r.ID]++
	g.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	d := arms.Decision{ID: r.ID, Arm: "fake", Determining: []string{},
		Raw: []arms.Raw{{Question: "gate", Reply: json.RawMessage(`{}`)}},
		Cedar: []arms.CedarEval{{Query: pdp.Query{Principal: pdp.Entity{Type: "Agent", ID: "a0"}, Action: "Action::\"x\"",
			Resource: pdp.Entity{Type: "Dataset", ID: "d"}}}}}
	if g.stopAt[r.ID] {
		d.Error = "per-run cap: test"
	}
	if e, ok := g.errAt[r.ID]; ok {
		d.Error = e
	}
	if arms.RecordOf(ctx) != r.ID {
		d.Error = "record label " + arms.RecordOf(ctx)
	}
	return d
}

func lines[T any](t *testing.T, path string) []T {
	t.Helper()
	out, err := corpus.ReadJSONL[T](path)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRunRanges(t *testing.T) {
	dir := testCorpus(t)
	for _, c := range []struct {
		offset, limit, conc, start, end int
	}{
		{0, 0, 1, 0, n},
		{0, 0, 8, 0, n},
		{5, 10, 4, 5, 15},
		{30, 100, 3, 30, n},
		{0, 7, 1, 0, 7},
	} {
		t.Run(fmt.Sprintf("%d+%d/%d", c.offset, c.limit, c.conc), func(t *testing.T) {
			g := &fakeGate{calls: map[string]int{}}
			res := t.TempDir()
			m, err := Run(context.Background(), g, Options{CorpusDir: dir, ResultsDir: res,
				Offset: c.offset, Limit: c.limit, Concurrency: c.conc})
			if err != nil {
				t.Fatal(err)
			}
			if m.RangeStart != c.start || m.RangeEnd != c.end || m.Records != c.end-c.start ||
				m.Decided != m.Records || m.Errors != 0 || m.Stopped != "" || m.Concurrency != c.conc || m.Host == "" {
				t.Fatalf("manifest %+v", m)
			}
			if len(g.calls) != c.end-c.start {
				t.Fatalf("%d records decided, want %d", len(g.calls), c.end-c.start)
			}
			for i := c.start; i < c.end; i++ {
				if id := fmt.Sprintf("r%04d", i); g.calls[id] != 1 {
					t.Fatalf("%s decided %d times", id, g.calls[id])
				}
			}
			if c.conc > 1 && g.peak.Load() < 2 {
				t.Errorf("peak concurrency %d", g.peak.Load())
			}
			out := filepath.Join(res, m.Run)
			seen := map[string]bool{}
			for _, d := range lines[arms.Decision](t, filepath.Join(out, "decisions.jsonl")) {
				if seen[d.ID] {
					t.Fatalf("%s written twice", d.ID)
				}
				seen[d.ID] = true
			}
			if len(seen) != m.Records || len(lines[map[string]any](t, filepath.Join(out, "raw.jsonl"))) != m.Records {
				t.Fatalf("%d decision lines, want %d", len(seen), m.Records)
			}
			// Quill stoop numbers are 1-based corpus indexes.
			qs := lines[struct {
				Stoop    int                       `json:"stoop_number"`
				Observed map[string]map[string]any `json:"observed_state"`
			}](t, filepath.Join(out, "quill.jsonl"))
			for _, q := range qs {
				if want := fmt.Sprintf("r%04d", q.Stoop-1); q.Observed["record"]["id"] != want {
					t.Fatalf("stoop %d is record %v", q.Stoop, q.Observed["record"]["id"])
				}
			}
			if len(qs) != m.Records {
				t.Fatalf("%d quill entries", len(qs))
			}
			var disk Manifest
			b, _ := os.ReadFile(filepath.Join(out, "manifest.json"))
			if err := json.Unmarshal(b, &disk); err != nil || disk.RangeEnd != c.end || disk.Finished == "" {
				t.Fatalf("manifest on disk %s", b)
			}
		})
	}
}

func TestRunSpendStop(t *testing.T) {
	dir := testCorpus(t)
	for _, conc := range []int{1, 4} {
		g := &fakeGate{calls: map[string]int{}, stopAt: map[string]bool{"r0010": true}}
		m, err := Run(context.Background(), g, Options{CorpusDir: dir, ResultsDir: t.TempDir(), Concurrency: conc})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(m.Stopped, "per-run cap") || m.Decided >= n || m.Errors != 1 {
			t.Fatalf("concurrency %d: manifest %+v", conc, m)
		}
		for id, k := range g.calls {
			if k != 1 {
				t.Fatalf("%s decided %d times", id, k)
			}
		}
		// Only work already started may finish after the stop.
		if len(g.calls) > 11+conc {
			t.Fatalf("concurrency %d: %d records started after a stop at the 11th", conc, len(g.calls))
		}
		if conc == 1 && (m.Decided != 10 || len(g.calls) != 11) {
			t.Fatalf("sequential: decided %d, started %d; want 10, 11", m.Decided, len(g.calls))
		}
	}
}

// TestRunRepairableErrors checks that the manifest counts the errors a
// re-run would re-ask apart from the ones that stay denials.
func TestRunRepairableErrors(t *testing.T) {
	g := &fakeGate{calls: map[string]int{}, errAt: map[string]string{
		"r0001": "503 Service Unavailable",
		"r0002": "Post \"http://127.0.0.1:8000/v1/systemone\": read: connection reset by peer",
		"r0003": "no PIP question for x",
	}}
	m, err := Run(context.Background(), g, Options{CorpusDir: testCorpus(t), ResultsDir: t.TempDir(), Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	if m.Errors != 3 || m.RepairableErrors != 2 || m.Decided != m.Records {
		t.Fatalf("errors %d, repairable %d, decided %d/%d; want 3, 2, all", m.Errors, m.RepairableErrors, m.Decided, m.Records)
	}
}

func TestRunBadRange(t *testing.T) {
	dir := testCorpus(t)
	if _, err := Run(context.Background(), &fakeGate{calls: map[string]int{}}, Options{CorpusDir: dir, ResultsDir: t.TempDir(), Offset: n}); err == nil {
		t.Fatal("want error for an offset past the corpus")
	}
}

func TestRepairable(t *testing.T) {
	for e, want := range map[string]bool{
		"": false,
		`decisions API: 429 Too Many Requests: {"error":{"message":"Rate limit reached"}}`:                true,
		`decisions API: 429 Too Many Requests: {"error":{"type":"insufficient_quota"}}`:                   false,
		`decisions API: 503 Service Unavailable: overloaded`:                                              true,
		`Post "https://api.openai.com/v1/decisions": context deadline exceeded (Client.Timeout exceeded)`: true,
		`decisions API: 1 answers expected, got 2`:                                                        false,
		`decisions API: 401 Unauthorized`:                                                                 false,
		`strands: question gate: noul 1.2 out of range`:                                                   false,
	} {
		if got := Repairable(e); got != want {
			t.Errorf("Repairable(%q) = %v, want %v", e, got, want)
		}
	}
}

func TestRepairableBedrock(t *testing.T) {
	for e, want := range map[string]bool{
		"bedrock: operation error Bedrock Runtime: InvokeModel, https response error StatusCode: 503, RequestID: x, ServiceUnavailableException: Bedrock is unable to process your request.": true,
		"bedrock: failed to get rate limit token, retry quota exceeded, 1 available, 5 requested":                                                                                            true,
		"bedrock: operation error Bedrock Runtime: InvokeModel, https response error StatusCode: 400, RequestID: x, ValidationException: malformed":                                          false,
	} {
		if got := Repairable(e); got != want {
			t.Errorf("Repairable(%q) = %v, want %v", e, got, want)
		}
	}
}
