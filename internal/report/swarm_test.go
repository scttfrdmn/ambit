package report

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/cedarq"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/lki/quill"
	"github.com/scttfrdmn/ambit/internal/swarm"
)

// synthRun describes a synthetic run: per-call gate latency (ms) and per-task
// critical path (ms) as functions of the task's delegation depth.
type synthRun struct {
	arm    string
	n      int
	serial bool
	slots  int
	scaled bool
	// gate is a call's gate latency in ms; path the task's critical path in
	// ms, given its depth. Both get ±10% noise.
	gate       func(depth int) float64
	path       func(depth int) float64
	repairable int
}

// tasksAt is the task count of a synthetic run at N: N/8, at least 4, as
// the default shape gives about.
func tasksAt(n int) int { return max(4, n/8) }

func (s synthRun) run(seed uint64) *SwarmRun {
	r := rand.New(rand.NewPCG(seed, 7))
	noise := func(v float64) float64 { return v * (0.9 + 0.2*r.Float64()) }
	nt := tasksAt(s.n)
	run := &SwarmRun{Manifest: swarm.Manifest{Arm: s.arm, Kind: swarm.ManifestKind, Script: swarm.Default(1, s.n, 4),
		Serial: s.serial, ServerSlots: s.slots, ScaledServers: s.scaled, Tasks: nt, RepairableErrors: s.repairable}}
	for i := range nt {
		id := fmt.Sprintf("t%05d", i)
		d := i % 4
		run.Tasks = append(run.Tasks, swarm.TaskResult{ID: id, MaxDepth: d, CriticalNS: int64(noise(s.path(d)) * 1e6)})
		for k := range 8 {
			run.Calls = append(run.Calls, swarm.Call{ID: fmt.Sprintf("%s.c%d", id, k), Task: id, Depth: d,
				GateNS: int64(noise(s.gate(d)) * 1e6), Truth: corpus.Truth{Verdict: corpus.Allow, Class: corpus.ClassWithin}})
		}
	}
	run.Manifest.Gated = len(run.Calls)
	return run
}

func constMS(v float64) func(int) float64 { return func(int) float64 { return v } }

// perLevel is a critical path that grows by step ms per delegation level.
func perLevel(base, step float64) func(int) float64 {
	return func(d int) float64 { return base + step*float64(d) }
}

// registered builds every registered arm at every N, B fast and flat, A
// slow and compounding, then applies the overrides.
func registered(over map[string]func(s *synthRun)) []*SwarmRun {
	var runs []*SwarmRun
	seed := uint64(1)
	add := func(s synthRun) {
		if f := over[s.arm]; f != nil {
			f(&s)
		}
		seed++
		runs = append(runs, s.run(seed))
	}
	for _, n := range []int{10, 100, 1000, 10000} {
		for _, a := range swarmBArms {
			add(synthRun{arm: a, n: n, gate: constMS(1), path: perLevel(6, 1)})
		}
		for _, a := range swarmAArms {
			add(synthRun{arm: a, n: n, gate: constMS(200), path: perLevel(800, 200)})
		}
	}
	return runs
}

func hscale(t *testing.T, runs []*SwarmRun) (map[string]any, string) {
	t.Helper()
	md, data := BuildSwarm(runs, Options{Resamples: 300})
	return data["h_scale"].(map[string]any), md
}

func partOf(t *testing.T, rows []map[string]any, arm, key string) part {
	t.Helper()
	for _, r := range rows {
		if r["arm"] == arm {
			return r[key].(part)
		}
	}
	t.Fatalf("no row for %s", arm)
	return part{}
}

func pairsOf(h map[string]any) map[string]string {
	out := map[string]string{}
	for _, p := range h["pairs"].([]map[string]any) {
		out[p["b"].(string)+"/"+p["a"].(string)] = p["h_scale"].(string)
	}
	return out
}

// TestHScaleHolds: B flat and fast, A slow and compounding: every one of
// the 25 registered pairs holds.
func TestHScaleHolds(t *testing.T) {
	h, md := hscale(t, registered(nil))
	pairs := pairsOf(h)
	if len(pairs) != 25 {
		t.Fatalf("%d pairs, want 25", len(pairs))
	}
	for k, v := range pairs {
		if v != "holds" {
			t.Errorf("pair %s: %s\n%s", k, v, md)
		}
	}
	if !strings.Contains(md, "25 of 25 pairs hold") {
		t.Errorf("summary missing:\n%s", md)
	}
}

// TestBNotFlat: a B arm whose p50 triples from N = 100 to N = 10,000 is not
// flat, and every pair with it fails; the others still hold.
func TestBNotFlat(t *testing.T) {
	runs := registered(map[string]func(*synthRun){"b-4b": func(s *synthRun) {
		if s.n == 10000 {
			s.gate = constMS(3)
		}
	}})
	h, md := hscale(t, runs)
	if p := partOf(t, h["b_flat"].([]map[string]any), "b-4b", "flat"); p.Value != partNo {
		t.Errorf("b-4b flat %+v\n%s", p, md)
	}
	if p := partOf(t, h["b_flat"].([]map[string]any), "b0", "flat"); p.Value != partYes {
		t.Errorf("b0 flat %+v", p)
	}
	for k, v := range pairsOf(h) {
		want := "holds"
		if strings.HasPrefix(k, "b-4b/") {
			want = "fails"
		}
		if v != want {
			t.Errorf("pair %s: %s, want %s", k, v, want)
		}
	}
	// A ratio just above 2 is not flat.
	runs = registered(map[string]func(*synthRun){"b-4b": func(s *synthRun) {
		if s.n == 10000 {
			s.gate = constMS(2.1)
		}
	}})
	h, _ = hscale(t, runs)
	if p := partOf(t, h["b_flat"].([]map[string]any), "b-4b", "flat"); p.Value != partNo {
		t.Errorf("b-4b at ratio 2.1: flat %+v, want no", p)
	}
}

// TestBaseIsN100: the flat rule divides by N = 100, not N = 10.
func TestBaseIsN100(t *testing.T) {
	runs := registered(map[string]func(*synthRun){"b0": func(s *synthRun) {
		if s.n == 10 {
			s.gate = constMS(0.1) // a tenth of the rest: N = 10 must not matter
		}
	}})
	h, _ := hscale(t, runs)
	if p := partOf(t, h["b_flat"].([]map[string]any), "b0", "flat"); p.Value != partYes {
		t.Errorf("b0 flat %+v: the base must be N = 100", p)
	}
}

// TestANotCompounding: an A arm whose critical path does not grow with
// depth does not compound; nor does one that grows no faster than a B arm,
// since the paired difference must exceed every B arm's slope.
func TestANotCompounding(t *testing.T) {
	runs := registered(map[string]func(*synthRun){
		"a-local": func(s *synthRun) { s.path = constMS(1600) },
		"a-d3":    func(s *synthRun) { s.path = perLevel(800, 2) },
		"b-hosted": func(s *synthRun) {
			if s.n == 1000 {
				s.path = perLevel(6, 2)
			}
		},
	})
	h, md := hscale(t, runs)
	rows := h["a_compounds"].([]map[string]any)
	if p := partOf(t, rows, "a-local", "compounds"); p.Value != partNo {
		t.Errorf("a-local compounds %+v\n%s", p, md)
	}
	// a-d3's slope (2 ms/level) is above zero and above b0's (1), but not
	// above b-hosted's (2): it does not compound.
	if p := partOf(t, rows, "a-d3", "compounds"); p.Value != partNo {
		t.Errorf("a-d3 compounds %+v\n%s", p, md)
	}
	if p := partOf(t, rows, "a-hosted", "compounds"); p.Value != partYes {
		t.Errorf("a-hosted compounds %+v", p)
	}
	pairs := pairsOf(h)
	for _, b := range swarmBArms {
		if pairs[b+"/a-local"] != "fails" || pairs[b+"/a-d3"] != "fails" || pairs[b+"/a-hosted"] != "holds" {
			t.Errorf("pairs with %s: a-local %s, a-d3 %s, a-hosted %s", b, pairs[b+"/a-local"], pairs[b+"/a-d3"], pairs[b+"/a-hosted"])
		}
	}
}

// TestP50Thresholds: at N = 1,000 the rules read the interval bounds, not
// the point estimates.
func TestP50Thresholds(t *testing.T) {
	runs := registered(map[string]func(*synthRun){
		// p50 about 4.99 ms: the point is under 5, the upper bound is not.
		"b-strands": func(s *synthRun) { s.gate = constMS(4.99) },
		// p50 about 150.2 ms: the point is over 150, the lower bound is not.
		"a-strands": func(s *synthRun) { s.gate = constMS(150.2) },
	})
	h, md := hscale(t, runs)
	rows := h["p50_at_1000"].([]map[string]any)
	if p := partOf(t, rows, "b-strands", "meets"); p.Value != partNo {
		t.Errorf("b-strands meets %+v\n%s", p, md)
	}
	if p := partOf(t, rows, "a-strands", "meets"); p.Value != partNo {
		t.Errorf("a-strands meets %+v", p)
	}
	if p := partOf(t, rows, "b0", "meets"); p.Value != partYes {
		t.Errorf("b0 meets %+v", p)
	}
}

// TestInstrumentFailure: above 1% repairable errors a run decides nothing;
// at exactly 1% it still does.
func TestInstrumentFailure(t *testing.T) {
	runs := registered(map[string]func(*synthRun){
		"a-hosted": func(s *synthRun) {
			if s.n == 1000 {
				s.repairable = tasksAt(1000)*8/100 + 1
			}
		},
		"a-safeguard": func(s *synthRun) {
			if s.n == 1000 {
				s.repairable = tasksAt(1000) * 8 / 100 // exactly 1%
			}
		},
	})
	h, md := hscale(t, runs)
	rows := h["p50_at_1000"].([]map[string]any)
	if p := partOf(t, rows, "a-hosted", "meets"); p.Value != partInstrument {
		t.Errorf("a-hosted meets %+v\n%s", p, md)
	}
	if p := partOf(t, rows, "a-safeguard", "meets"); p.Value != partYes {
		t.Errorf("a-safeguard meets %+v (1%% is not above 1%%)", p)
	}
	if v := pairsOf(h)["b0/a-hosted"]; v != "undetermined" {
		t.Errorf("b0/a-hosted %s, want undetermined", v)
	}
	if !strings.Contains(md, "**failure: re-run**") {
		t.Errorf("runs table does not flag the failure")
	}
}

// TestNotMeasured: missing runs give "not measured" parts, never a verdict.
func TestNotMeasured(t *testing.T) {
	var runs []*SwarmRun
	for _, r := range registered(nil) {
		if r.Manifest.Arm == "b-safeguard" && r.Manifest.Script.Agents == 10000 {
			continue
		}
		runs = append(runs, r)
	}
	h, _ := hscale(t, runs)
	if p := partOf(t, h["b_flat"].([]map[string]any), "b-safeguard", "flat"); p.Value != partNotMeasured {
		t.Errorf("b-safeguard flat %+v", p)
	}
	if v := pairsOf(h)["b-safeguard/a-local"]; v != "undetermined" {
		t.Errorf("b-safeguard/a-local %s", v)
	}
}

// TestSerialProbeResolution: a serial reference p50 at N = 100 that already
// misses the threshold is noted beside the verdict.
func TestSerialProbeResolution(t *testing.T) {
	runs := registered(nil)
	runs = append(runs, synthRun{arm: "a-local", n: 100, serial: true, gate: constMS(90), path: perLevel(300, 90)}.run(99),
		synthRun{arm: "b0", n: 100, serial: true, gate: constMS(0.5), path: perLevel(3, 0.5)}.run(98))
	h, md := hscale(t, runs)
	rows := h["p50_at_1000"].([]map[string]any)
	for _, r := range rows {
		note, _ := r["probe_resolution"].(string)
		switch r["arm"] {
		case "a-local":
			if !strings.Contains(note, "not above 150 ms") {
				t.Errorf("a-local note %q\n%s", note, md)
			}
		case "b0":
			if note != "" {
				t.Errorf("b0 note %q", note)
			}
		}
	}
	// Serial runs carry no verdict: the pair still holds.
	if v := pairsOf(h)["b0/a-local"]; v != "holds" {
		t.Errorf("b0/a-local %s", v)
	}
}

// TestScaledServers: a run marked as a scaled-server secondary is kept
// apart from the primary run at the same N and from the verdicts, whatever
// the slot counts recorded.
func TestScaledServers(t *testing.T) {
	runs := registered(nil)
	for _, r := range runs {
		if r.Manifest.Arm == "a-d3" || r.Manifest.Arm == "b-4b" {
			r.Manifest.ServerSlots = 4
		}
	}
	runs = append(runs,
		// Much faster with 4x the slots: were it scored, a-d3 would fail.
		synthRun{arm: "a-d3", n: 1000, slots: 16, scaled: true, gate: constMS(20), path: constMS(100)}.run(77),
		synthRun{arm: "b-4b", n: 1000, slots: 16, scaled: true, gate: constMS(9), path: perLevel(6, 1)}.run(78))
	md, data := BuildSwarm(runs, Options{Resamples: 300})
	for k, v := range pairsOf(data["h_scale"].(map[string]any)) {
		if v != "holds" {
			t.Errorf("pair %s: %s; a scaled run entered the verdicts", k, v)
		}
	}
	sc, _ := data["scaled_servers"].([]map[string]any)
	if len(sc) != 2 || !strings.Contains(md, "## Scaled servers (secondary)") {
		t.Fatalf("scaled table: %v\n%s", sc, md)
	}
	for _, r := range sc {
		if r["server_slots"] != 16 || r["primary_p50_ms"].(map[string]any)["est"] == nil {
			t.Errorf("scaled row %v", r)
		}
	}
}

func TestSelectQ(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 3))
	for n := 1; n < 60; n++ {
		xs := make([]float64, n)
		for i := range xs {
			xs[i] = float64(r.IntN(10))
		}
		for _, p := range []float64{0, 0.5, 0.95, 1} {
			if got, want := selectQ(append([]float64(nil), xs...), p), percentile(xs, p); got != want {
				t.Fatalf("n=%d p=%v: %v, want %v", n, p, got, want)
			}
		}
	}
	if !math.IsNaN(selectQ(nil, 0.5)) {
		t.Error("empty quantile is not NaN")
	}
}

// --- Through the harness ---

// namedGate is B0 under another arm's name, with an added latency. As an
// A arm it drops the Cedar evaluations, so it writes no quill.
type namedGate struct {
	arm   string
	inner arms.Gate
	delay time.Duration
}

func (g *namedGate) Name() string { return g.arm }

func (g *namedGate) Decide(ctx context.Context, r corpus.Record) arms.Decision {
	time.Sleep(g.delay)
	d := g.inner.Decide(ctx, r)
	d.Arm = g.arm
	if isAArm(g.arm) {
		d.Cedar, d.Determining = nil, []string{}
	}
	return d
}

// TestSwarmReport runs a "B" and an "A" through the harness, writes them as
// ambit swarm does (quill included), loads them, and renders the report.
func TestSwarmReport(t *testing.T) {
	p, err := cedarq.LoadPolicies(filepath.Join("..", "..", "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, g := range []*namedGate{
		{arm: "b0", inner: &arms.Cedar{Arm: "b0", Policies: p}},
		{arm: "a-local", inner: &arms.Cedar{Arm: "b0", Policies: p}, delay: time.Millisecond},
	} {
		for _, n := range []int{10, 100} {
			s, err := swarm.NewScript(swarm.Default(1, n, 2))
			if err != nil {
				t.Fatal(err)
			}
			m := swarm.Manifest{Run: fmt.Sprintf("%s-swarm-%d", g.arm, n), Arm: g.arm, Kind: swarm.ManifestKind,
				Script: s.Config, Tasks: len(s.Tasks), Started: time.Now().UTC().Format(time.RFC3339Nano), Finished: "done"}
			out := filepath.Join(dir, m.Run)
			qw, err := quill.NewWriter(filepath.Join(out, "quill.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			res, err := swarm.Run(context.Background(), g, s, swarm.Options{Quill: qw, Run: m.Run})
			qw.Close()
			if err != nil {
				t.Fatal(err)
			}
			m.Gated, m.WallNS, m.QuillEntries = len(res.Calls), res.WallNS, res.QuillEntries
			if err := swarm.WriteManifest(out, m); err != nil {
				t.Fatal(err)
			}
			if err := swarm.WriteResults(out, res); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A stopped run is not loaded.
	for name, m := range map[string]swarm.Manifest{
		"stopped": {Arm: "b0", Kind: swarm.ManifestKind, Script: swarm.Default(1, 100, 2), Started: "z", Finished: "done", Stopped: "per-run cap"},
	} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := swarm.WriteManifest(filepath.Join(dir, name), m); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := LoadSwarm(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 {
		t.Fatalf("loaded %d runs, want 4", len(runs))
	}
	for _, r := range runs {
		b := isBArm(r.Manifest.Arm)
		if b && len(r.QuillDet) != len(r.Calls) || !b && len(r.QuillDet) != 0 {
			t.Errorf("%s: %d quill entries for %d calls", r.Manifest.Run, len(r.QuillDet), len(r.Calls))
		}
	}
	md, data := BuildSwarm(runs, Options{Resamples: 100})
	for _, s := range []string{"## Critical path by delegation depth", "false-allow", "### H-scale by pair", "## Auditability", "## Cost"} {
		if !strings.Contains(md, s) {
			t.Errorf("report is missing %q:\n%s", s, md)
		}
	}
	audit := data["auditability"].([]map[string]any)
	if len(audit) != 2 {
		t.Fatalf("auditability rows %v", audit)
	}
	for _, a := range audit {
		in := a["audit_excluding_outside"].(Interval)
		if a["quill_disagrees"] != 0 || in.N == 0 || in.K != in.N {
			t.Errorf("b0 audit %v: every in-ambit Cedar denial names a policy", a)
		}
	}
}
