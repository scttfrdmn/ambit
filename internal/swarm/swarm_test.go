package swarm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/cedarq"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/oracle"
	"github.com/scttfrdmn/ambit/internal/vocab"
)

func b0(t *testing.T) arms.Gate {
	t.Helper()
	p, err := cedarq.LoadPolicies(filepath.Join("..", "..", "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	return &arms.Cedar{Arm: "b0", Policies: p}
}

func script(t *testing.T, cfg Config) *Script {
	t.Helper()
	s, err := NewScript(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// uncapped is a script whose caps never bind, so that a concurrent run's
// decisions do not depend on the order spend accrues.
func uncapped(n int) Config {
	cfg := Default(11, n, 4)
	cfg.SwarmCapPercent, cfg.TaskCapScale = 1_000_000, 1_000_000
	return cfg
}

// slowGate is another gate with a fixed added latency. It counts the most
// decisions in flight at once.
type slowGate struct {
	inner    arms.Gate
	delay    time.Duration
	allowAll bool
	inflight atomic.Int32
	peak     atomic.Int32
}

func (g *slowGate) Name() string { return "slow" }

func (g *slowGate) Decide(ctx context.Context, r corpus.Record) arms.Decision {
	cur := g.inflight.Add(1)
	defer g.inflight.Add(-1)
	for p := g.peak.Load(); cur > p && !g.peak.CompareAndSwap(p, cur); p = g.peak.Load() {
	}
	time.Sleep(g.delay)
	d := g.inner.Decide(ctx, r)
	if g.allowAll {
		d.Allow = true
	}
	return d
}

func TestScript(t *testing.T) {
	for _, n := range []int{10, 100, 1000} {
		s := script(t, Default(3, n, 4))
		again := script(t, Default(3, n, 4))
		h1, _ := s.SHA256()
		h2, _ := again.SHA256()
		if h1 != h2 {
			t.Fatalf("N=%d: same seed, scripts differ", n)
		}
		other := script(t, Default(4, n, 4))
		if h3, _ := other.SHA256(); h3 == h1 {
			t.Fatalf("N=%d: different seeds, same script", n)
		}
		scripted, deep, probes := 0, 0, 0
		ids := map[string]bool{}
		for i, a := range s.Agents {
			if ids[a.ID] {
				t.Fatalf("agent ID %s repeats", a.ID)
			}
			ids[a.ID] = true
			if len(a.Work) != 4 {
				t.Errorf("agent %d has %d work calls", i, len(a.Work))
			}
			if a.Depth != len(a.Chain)-1 || a.Chain[len(a.Chain)-1].ID != a.ID {
				t.Errorf("agent %d: depth %d, chain %v", i, a.Depth, a.Chain)
			}
			for _, p := range a.Chain[:len(a.Chain)-1] {
				if !slices.Contains(vocab.Delegators, p.Role) {
					t.Errorf("agent %d has a %s above it", i, p.Role)
				}
			}
			switch {
			case a.Probe:
				probes++
				if a.Depth != vocab.MaxDepth+1 {
					t.Errorf("probe %d at depth %d", i, a.Depth)
				}
			default:
				scripted++
				if a.Depth > vocab.MaxDepth {
					t.Errorf("agent %d at depth %d", i, a.Depth)
				}
			}
			if a.Depth == vocab.MaxDepth {
				deep++
			}
		}
		if scripted != n {
			t.Errorf("N=%d: %d scripted agents", n, scripted)
		}
		if n >= 100 && (deep == 0 || probes == 0) {
			t.Errorf("N=%d: %d agents at depth 3, %d probes", n, deep, probes)
		}
		if s.SwarmCap <= 0 || s.SwarmCap >= s.UncappedAllowed {
			t.Errorf("N=%d: swarm cap %d, uncapped allowed spend %d", n, s.SwarmCap, s.UncappedAllowed)
		}
	}
}

// expected returns the IDs of the calls a run must record: every call of
// the roots and of every agent whose creating delegation was allowed.
func expected(s *Script, calls []Call) map[string]bool {
	allowed := map[string]bool{}
	for _, c := range calls {
		allowed[c.ID] = c.Decision.Allow
	}
	want := map[string]bool{}
	var walk func(a int)
	walk = func(a int) {
		for _, st := range s.Agents[a].Work {
			want[st.ID] = true
		}
		for _, st := range s.Agents[a].Delegations {
			want[st.ID] = true
			if allowed[st.ID] {
				walk(st.Child)
			}
		}
	}
	for _, t := range s.Tasks {
		walk(t.Root)
	}
	return want
}

// checkRun checks what holds of any run: every call recorded exactly once,
// the chain each gate saw is the calling agent's, and the truth is the
// oracle's on the facts the gate saw.
func checkRun(t *testing.T, s *Script, res Result) {
	t.Helper()
	seen := map[string]int{}
	for _, c := range res.Calls {
		seen[c.ID]++
	}
	want := expected(s, res.Calls)
	for id, n := range seen {
		if n != 1 {
			t.Errorf("call %s recorded %d times", id, n)
		}
		if !want[id] {
			t.Errorf("call %s recorded but not expected", id)
		}
	}
	for id := range want {
		if seen[id] == 0 {
			t.Errorf("call %s expected but not recorded", id)
		}
	}
	byAgent := map[string]Agent{}
	for _, a := range s.Agents {
		byAgent[a.ID] = a
	}
	ran := 0
	for _, c := range res.Calls {
		a := byAgent[c.Agent]
		f := c.Record.Facts
		if !slices.Equal(f.Chain, a.Chain) || f.Depth != a.Depth || c.Depth != a.Depth {
			t.Errorf("%s: gate saw chain %v at depth %d; agent's chain is %v", c.ID, f.Chain, f.Depth, a.Chain)
		}
		if c.Decision.ID != c.ID || c.Record.ID != c.ID || c.Truth.ID != c.ID {
			t.Errorf("%s: decision %s, record %s, truth %s", c.ID, c.Decision.ID, c.Record.ID, c.Truth.ID)
		}
		if v := oracle.Verdict(f, c.Semantic); v != c.Truth.Verdict {
			t.Errorf("%s: truth %s, oracle %s", c.ID, c.Truth.Verdict, v)
		}
		if f.TaskCap != s.Tasks[a.Task].TaskCap || f.SwarmCap != s.SwarmCap {
			t.Errorf("%s: caps %d, %d", c.ID, f.TaskCap, f.SwarmCap)
		}
	}
	for _, tr := range res.Tasks {
		ran += tr.Ran
		var sum int64
		for _, v := range tr.ByDepthNS {
			sum += v
		}
		if sum != tr.CriticalNS {
			t.Errorf("task %s: by-depth split sums to %d, path is %d", tr.ID, sum, tr.CriticalNS)
		}
	}
	if ran != res.AgentsRan {
		t.Errorf("tasks count %d agents ran, run counts %d", ran, res.AgentsRan)
	}
	var spent int64
	for _, c := range res.Calls {
		if c.Decision.Allow {
			spent += c.Record.Facts.EstCost
		}
	}
	if spent != res.SwarmSpent {
		t.Errorf("allowed calls cost %d, swarm spent %d", spent, res.SwarmSpent)
	}
}

// canonical is a run's calls with what varies between runs removed:
// latencies always, and spend facts if the run was concurrent.
func canonical(t *testing.T, calls []Call, spend bool) string {
	t.Helper()
	var out []any
	for _, c := range calls {
		f := c.Record.Facts
		if !spend {
			f.TaskSpent, f.SwarmSpent = 0, 0
		}
		out = append(out, []any{c.ID, c.Agent, c.Depth, c.Kind, f, c.Record.Payload, c.Decision.Allow, c.Decision.Determining,
			c.Truth.Verdict, c.Truth.Violations})
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestB0Swarm(t *testing.T) {
	g := b0(t)
	for _, n := range []int{10, 100} {
		// Serial runs are reproducible to the spend.
		s := script(t, Default(5, n, 4))
		r1, err := Run(context.Background(), g, s, Options{Serial: true})
		if err != nil {
			t.Fatal(err)
		}
		r2, err := Run(context.Background(), g, script(t, Default(5, n, 4)), Options{Serial: true})
		if err != nil {
			t.Fatal(err)
		}
		checkRun(t, s, r1)
		if canonical(t, r1.Calls, true) != canonical(t, r2.Calls, true) {
			t.Errorf("N=%d: two serial runs of one seed differ", n)
		}
		for _, c := range r1.Calls {
			if c.Decision.Allow && c.Truth.Verdict == corpus.Deny {
				t.Errorf("N=%d: B0 allowed %s, which the oracle denies", n, c.ID)
			}
		}
		if r1.ProbesRan != 0 {
			t.Errorf("N=%d: B0 let %d probes run", n, r1.ProbesRan)
		}

		// Concurrent runs, with caps that never bind, decide the same calls
		// the same way; only the spend each gate saw differs.
		u := script(t, uncapped(n))
		ref, err := Run(context.Background(), g, u, Options{Serial: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, conc := range []int{0, 3} {
			res, err := Run(context.Background(), g, u, Options{Concurrency: conc})
			if err != nil {
				t.Fatal(err)
			}
			checkRun(t, u, res)
			if canonical(t, res.Calls, false) != canonical(t, ref.Calls, false) {
				t.Errorf("N=%d, concurrency %d: concurrent run differs from the serial run", n, conc)
			}
		}
	}
}

// TestLiveSpend: the spend each gate sees is the cost of the calls allowed
// before it, and the swarm crosses its cap partway through.
func TestLiveSpend(t *testing.T) {
	s := script(t, Default(5, 100, 4))
	res, err := Run(context.Background(), b0(t), s, Options{Serial: true})
	if err != nil {
		t.Fatal(err)
	}
	calls := slices.Clone(res.Calls)
	slices.SortFunc(calls, func(a, b Call) int { return int(a.StartNS - b.StartNS) })
	var swarm int64
	task := map[string]int64{}
	capDenies, crossed := 0, false
	for _, c := range calls {
		f := c.Record.Facts
		if f.SwarmSpent != swarm || f.TaskSpent != task[c.Task] {
			t.Fatalf("%s: gate saw spend %d/%d, allowed so far %d/%d", c.ID, f.TaskSpent, f.SwarmSpent, task[c.Task], swarm)
		}
		if slices.Contains(c.Truth.Violations, oracle.RuleSwarmCap) {
			capDenies++
			if c.Decision.Allow {
				t.Errorf("%s crosses the swarm cap and was allowed", c.ID)
			}
		}
		if c.Decision.Allow {
			swarm += f.EstCost
			task[c.Task] += f.EstCost
		}
		if swarm > s.SwarmCap*9/10 {
			crossed = true
		}
	}
	if capDenies == 0 || !crossed {
		t.Fatalf("swarm cap %d never bound: %d cap denials, final spend %d", s.SwarmCap, capDenies, swarm)
	}
	if swarm > s.SwarmCap {
		t.Errorf("serial spend %d over cap %d", swarm, s.SwarmCap)
	}
	// A concurrent run accrues spend live too: every allowed cost is
	// counted once, whatever the order.
	cres, err := Run(context.Background(), b0(t), s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	checkRun(t, s, cres)
	n := 0
	for _, c := range cres.Calls {
		if slices.Contains(c.Truth.Violations, oracle.RuleSwarmCap) {
			n++
		}
	}
	if n == 0 {
		t.Errorf("concurrent run: the swarm cap never bound")
	}
}

// TestLatency: with a fixed gate latency, every call's gate time includes
// it, the critical path is that latency times its calls, and the in-flight
// cap holds.
func TestLatency(t *testing.T) {
	const delay = 3 * time.Millisecond
	for _, n := range []int{10, 100} {
		s := script(t, uncapped(n))
		g := &slowGate{inner: b0(t), delay: delay}
		res, err := Run(context.Background(), g, s, Options{Concurrency: 16})
		if err != nil {
			t.Fatal(err)
		}
		checkRun(t, s, res)
		if p := g.peak.Load(); p > 16 {
			t.Errorf("N=%d: %d gate calls in flight, cap 16", n, p)
		}
		for _, c := range res.Calls {
			if c.GateNS < delay.Nanoseconds() {
				t.Fatalf("%s: gate time %d under the injected %d", c.ID, c.GateNS, delay.Nanoseconds())
			}
		}
		for _, tr := range res.Tasks {
			if tr.CriticalNS < int64(tr.CriticalCalls)*delay.Nanoseconds() {
				t.Errorf("task %s: critical path %d for %d calls", tr.ID, tr.CriticalNS, tr.CriticalCalls)
			}
			// The path's calls: one delegation per level down to the agent
			// that ends it, then at most its work calls.
			if tr.CriticalCalls < tr.CriticalDepth || tr.CriticalCalls > tr.CriticalDepth+4 {
				t.Errorf("task %s: %d path calls ending at depth %d", tr.ID, tr.CriticalCalls, tr.CriticalDepth)
			}
			if tr.Ran > 1 && tr.WallNS < tr.CriticalNS/2 {
				t.Errorf("task %s: wall %d, critical path %d", tr.ID, tr.WallNS, tr.CriticalNS)
			}
		}
	}
}

// TestCriticalPath: with unit latencies the path's length is its call
// count, and it is the longest chain of delegations plus work calls.
func TestCriticalPath(t *testing.T) {
	s := script(t, uncapped(100))
	res, err := Run(context.Background(), b0(t), s, Options{Serial: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := range res.Calls {
		res.Calls[i].GateNS = 1
	}
	allowed := map[string]bool{}
	for _, c := range res.Calls {
		allowed[c.ID] = c.Decision.Allow
	}
	for i, tr := range CriticalPaths(s, res.Calls) {
		var longest func(a int) int64
		longest = func(a int) int64 {
			ag := s.Agents[a]
			best := int64(len(ag.Work))
			for _, st := range ag.Delegations {
				best = max(best, 1)
				if allowed[st.ID] {
					best = max(best, 1+longest(st.Child))
				}
			}
			return best
		}
		want := longest(s.Tasks[i].Root)
		if tr.CriticalNS != want || int64(tr.CriticalCalls) != want {
			t.Errorf("task %s: path %d ns over %d calls, want %d", tr.ID, tr.CriticalNS, tr.CriticalCalls, want)
		}
	}
}

// TestProbes: a gate that allows everything lets the too-deep delegations
// through; their sub-agents run below the depth limit, and the oracle
// denies every call they make.
func TestProbes(t *testing.T) {
	s := script(t, uncapped(100))
	g := &slowGate{inner: b0(t), allowAll: true}
	res, err := Run(context.Background(), g, s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	checkRun(t, s, res)
	probes := 0
	for _, a := range s.Agents {
		if a.Probe {
			probes++
		}
	}
	if probes == 0 || res.ProbesRan != probes {
		t.Fatalf("%d probes scripted, %d ran", probes, res.ProbesRan)
	}
	deep := 0
	for _, c := range res.Calls {
		if c.Probe {
			deep++
			if c.Depth != vocab.MaxDepth+1 || c.Truth.Verdict != corpus.Deny || !slices.Contains(c.Truth.Violations, oracle.RuleDepthMax) {
				t.Errorf("probe call %s: depth %d, truth %s %v", c.ID, c.Depth, c.Truth.Verdict, c.Truth.Violations)
			}
		}
	}
	if deep != probes*4 {
		t.Errorf("%d probe calls for %d probes", deep, probes)
	}
}
