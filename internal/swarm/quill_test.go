package swarm

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/lki/quill"
)

// modelGate stands for an Arm A gate: B0's verdicts, with no Cedar
// evaluations and no determining policies.
type modelGate struct{ inner arms.Gate }

func (g *modelGate) Name() string { return "a-fake" }

func (g *modelGate) Decide(ctx context.Context, r corpus.Record) arms.Decision {
	d := g.inner.Decide(ctx, r)
	d.Arm, d.Cedar, d.Determining = "a-fake", nil, []string{}
	return d
}

// TestQuill: a Cedar arm writes one quill entry per decision, matching the
// decision and naming its call; an Arm A gate writes none.
func TestQuill(t *testing.T) {
	for _, tc := range []struct {
		g     arms.Gate
		cedar bool
	}{{b0(t), true}, {&modelGate{b0(t)}, false}} {
		path := filepath.Join(t.TempDir(), "quill.jsonl")
		qw, err := quill.NewWriter(path)
		if err != nil {
			t.Fatal(err)
		}
		s := script(t, Default(5, 100, 4))
		res, err := Run(context.Background(), tc.g, s, Options{Quill: qw, Run: "test-run"})
		qw.Close()
		if err != nil {
			t.Fatal(err)
		}
		checkRun(t, s, res)
		es, err := quill.ReadAll(path)
		if err != nil && tc.cedar {
			t.Fatal(err)
		}
		if !tc.cedar {
			if len(es) != 0 || res.QuillEntries != 0 {
				t.Errorf("%s: %d quill entries", tc.g.Name(), len(es))
			}
			continue
		}
		if len(es) != len(res.Calls) || res.QuillEntries != len(es) {
			t.Fatalf("%d quill entries (%d counted) for %d calls", len(es), res.QuillEntries, len(res.Calls))
		}
		byID := map[string]Call{}
		for _, c := range res.Calls {
			byID[c.ID] = c
			if c.Decision.Cedar != nil {
				t.Fatalf("%s: Cedar evaluations kept in memory", c.ID)
			}
		}
		seen := map[string]bool{}
		for i, e := range es {
			id, _ := e.ObservedState["record"]["id"].(string)
			c, ok := byID[id]
			if !ok || seen[id] {
				t.Fatalf("entry %d names call %q (known %v, seen %v)", i, id, ok, seen[id])
			}
			seen[id] = true
			if e.CastID != "test-run" || e.StoopNumber != i+1 || e.EntryID == "" {
				t.Errorf("entry %d: cast %s, stoop %d, id %q", i, e.CastID, e.StoopNumber, e.EntryID)
			}
			want := map[bool]string{true: "Allow", false: "Deny"}[c.Decision.Allow]
			last := e.PDPDecisions[len(e.PDPDecisions)-1]
			if e.IntentOutcome != want || strings.Join(last.DeterminingPolicies, ",") != strings.Join(c.Decision.Determining, ",") {
				t.Errorf("%s: quill %s %v, decision %s %v", id, e.IntentOutcome, last.DeterminingPolicies, want, c.Decision.Determining)
			}
			if e.Principal.ID != c.Agent || e.Intent.Name != c.Record.Facts.Tool {
				t.Errorf("%s: quill principal %s intent %s; call agent %s tool %s", id, e.Principal.ID, e.Intent.Name, c.Agent, c.Record.Facts.Tool)
			}
			if sw := e.ObservedState["swarm"]; sw["task"] != c.Task || sw["agent"] != c.Agent {
				t.Errorf("%s: swarm state %v", id, sw)
			}
		}
	}
}

// flakyGate fails some calls: every third with a capacity error a re-run
// would re-ask, every fifth with one it would not.
type flakyGate struct {
	inner arms.Gate
	n     atomic.Int32
}

func (g *flakyGate) Name() string { return "flaky" }

func (g *flakyGate) Decide(ctx context.Context, r corpus.Record) arms.Decision {
	d := g.inner.Decide(ctx, r)
	switch n := g.n.Add(1); {
	case n%3 == 0:
		d.Allow, d.Error = false, "pip: 503 Service Unavailable"
	case n%5 == 0:
		d.Allow, d.Error = false, "pip: unparseable reply"
	}
	return d
}

// TestRepairableErrors: a gate error is a deny, and the run counts the
// repairable ones apart.
func TestRepairableErrors(t *testing.T) {
	s := script(t, uncapped(100))
	res, err := Run(context.Background(), &flakyGate{inner: b0(t)}, s, Options{Serial: true})
	if err != nil {
		t.Fatal(err)
	}
	checkRun(t, s, res)
	errs, rep := 0, 0
	for _, c := range res.Calls {
		if c.Decision.Error != "" {
			errs++
			if c.Decision.Allow {
				t.Errorf("%s: an error allowed", c.ID)
			}
			if strings.Contains(c.Decision.Error, "503") {
				rep++
			}
		}
	}
	if res.Errors != errs || res.RepairableErrors != rep || rep == 0 || rep == errs {
		t.Errorf("errors %d (repairable %d); counted %d (%d)", res.Errors, res.RepairableErrors, errs, rep)
	}
}

// TestSpendStop: a Stop reason halts the swarm; later calls are denied
// without asking the gate and are not recorded.
func TestSpendStop(t *testing.T) {
	s := script(t, uncapped(100))
	asked := 0
	g := &countGate{inner: b0(t), n: &asked}
	res, err := Run(context.Background(), g, s, Options{Serial: true, Stop: func(arms.Decision) string {
		if asked >= 10 {
			return "per-run cap reached"
		}
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != "per-run cap reached" || len(res.Calls) != 10 || asked != 10 {
		t.Errorf("stopped %q after %d calls, %d asked", res.Stopped, len(res.Calls), asked)
	}
}

type countGate struct {
	inner arms.Gate
	n     *int
}

func (g *countGate) Name() string { return "count" }

func (g *countGate) Decide(ctx context.Context, r corpus.Record) arms.Decision {
	*g.n++
	return g.inner.Decide(ctx, r)
}

// TestStream: with a Stream, every recorded call reaches calls.jsonl in the
// quill's order; a run cancelled mid-way keeps the calls it recorded, and
// its result still counts the agents that ran.
func TestStream(t *testing.T) {
	for _, cancelAt := range []int{0, 50} {
		dir := t.TempDir()
		qw, err := quill.NewWriter(filepath.Join(dir, "quill.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		st, err := NewStream(dir)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		s := script(t, Default(5, 1000, 4))
		var n atomic.Int64
		res, err := Run(ctx, b0(t), s, Options{Quill: qw, Run: "test-run", Stream: st, Concurrency: 4,
			Stop: func(arms.Decision) string {
				if ctx.Err() != nil {
					return "interrupted"
				}
				return ""
			},
			Progress: func(int, Call) {
				if n.Add(1) == int64(cancelAt) {
					cancel()
				}
			}})
		cancel()
		qw.Close()
		if cerr := st.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		if cancelAt == 0 && err != nil {
			t.Fatal(err)
		}
		if cancelAt > 0 && res.Stopped != "interrupted" {
			t.Fatalf("stopped %q, err %v", res.Stopped, err)
		}
		calls, err := corpus.ReadJSONL[Call](filepath.Join(dir, "calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		es, err := quill.ReadAll(filepath.Join(dir, "quill.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) == 0 || len(calls) != len(res.Calls) || len(es) != len(calls) || res.AgentsRan == 0 || len(res.Tasks) != len(s.Tasks) {
			t.Fatalf("cancel at %d: %d streamed, %d recorded, %d quill entries, %d agents ran, %d tasks",
				cancelAt, len(calls), len(res.Calls), len(es), res.AgentsRan, len(res.Tasks))
		}
		if cancelAt > 0 && len(calls) >= len(s.Agents) {
			t.Errorf("cancelled run streamed %d calls", len(calls))
		}
		for i, e := range es {
			if id, _ := e.ObservedState["record"]["id"].(string); id != calls[i].ID {
				t.Fatalf("line %d: quill %s, stream %s", i, id, calls[i].ID)
			}
		}
	}
}
