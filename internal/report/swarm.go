package report

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/lki/quill"
	"github.com/scttfrdmn/ambit/internal/swarm"
	"github.com/scttfrdmn/ambit/internal/vocab"
)

// G3 live-swarm report (#21, H-scale): "Arm B gate latency flat in agent
// count; Arm A compounds with delegation depth. At 1,000 concurrent agents:
// B p50 < 5 ms, A p50 > 150 ms." The decision rules are the G3
// registration's on #21, fixed before any counted run:
//
//  1. B flat: r = p50 gate latency at N = 10,000 ÷ p50 at N = 100; flat iff
//     the 95% upper bound of r ≤ 2.
//  2. A compounds: at N = 1,000, the least-squares slope of a task's
//     critical-path latency on its delegation depth, in ms per level; an A
//     arm compounds iff its slope's 95% lower bound is > 0 and the paired
//     (by task) difference to every B arm's slope has a 95% lower bound > 0.
//  3. At N = 1,000: a B arm meets "< 5 ms" iff its p50's 95% upper bound is
//     < 5 ms; an A arm meets "> 150 ms" iff its p50's 95% lower bound is
//     > 150 ms.
//  4. H-scale holds for a pair (B, A) iff B is flat and meets < 5 ms and A
//     compounds and meets > 150 ms. Every pair is reported with its parts.
//
// Intervals are 95% bootstrap over the swarm's tasks (DefaultResamples,
// 2,000), percentile method, as the corpus reports'. A "95% upper bound" is
// the interval's upper end, a "95% lower bound" its lower end.

// Registered H-scale constants (#21).
const (
	hScaleBaseN   = 100
	hScaleTopN    = 10000
	hScaleN       = 1000
	hScaleFlatMax = 2.0
	hScaleBMaxMS  = 5.0
	hScaleAMinMS  = 150.0
	// repairableMaxPercent: above 1% repairable errors among a run's gate
	// calls, the run is an instrument failure and is re-run.
	repairableMaxPercent = 1
	// d3TransferThreshold is A-d3's G2b-transferred threshold; its swarm
	// accuracy there is recomputed from the recorded P (exploratory).
	d3TransferThreshold = 0.9316
)

// The registered G3 arms: the ten G2 headline arms.
var (
	swarmBArms = []string{"b0", "b-4b", "b-hosted", "b-strands", "b-safeguard"}
	swarmAArms = []string{"a-local", "a-hosted", "a-strands", "a-d3", "a-safeguard"}
)

// onDemandUSDPerHour is the us-east-1 on-demand list price of the instance
// types G3 places runs on (truffle, 2026-10-24; as compute-cost).
var onDemandUSDPerHour = map[string]float64{
	"g6e.xlarge": 1.861, "g6.xlarge": 0.8048, "g6.2xlarge": 0.9776, "g5.xlarge": 1.006, "g4dn.xlarge": 0.526,
}

// SwarmRun is one swarm run: a complete one, scored, or an incomplete one,
// listed only.
type SwarmRun struct {
	Dir      string
	Manifest swarm.Manifest
	Calls    []swarm.Call
	Tasks    []swarm.TaskResult
	// QuillDet maps a call ID to whether its quill entry names a
	// determining policy; nil if the run has no quill.jsonl.
	QuillDet map[string]bool
	// RecordedCalls is, for an incomplete run, the calls its calls.jsonl
	// holds (streamed as the run went); its Calls and Tasks are not loaded.
	RecordedCalls int
}

// Incomplete reports whether the run did not finish: its manifest is
// unfinished (killed, or still running) or it stopped early. An incomplete
// run never enters a verdict.
func (r *SwarmRun) Incomplete() bool { return r.Manifest.Finished == "" || r.Manifest.Stopped != "" }

// InstrumentFailure reports whether the run's repairable errors exceed 1%
// of its gate calls (#21): it is re-run, and its numbers decide nothing.
func (r *SwarmRun) InstrumentFailure() bool {
	return r.Manifest.RepairableErrors*100 > repairableMaxPercent*r.Manifest.Gated
}

// LoadSwarm reads the latest complete swarm run of each arm at each N
// under dir, measured and serial reference runs apart, and every incomplete
// run (Incomplete) with the count of the calls it streamed. A run is
// complete if its manifest is finished and it did not stop early.
func LoadSwarm(dir string) ([]*SwarmRun, error) {
	mfs, err := filepath.Glob(filepath.Join(dir, "*", "manifest.json"))
	if err != nil {
		return nil, err
	}
	latest := map[string]*SwarmRun{}
	var incomplete []*SwarmRun
	for _, mf := range mfs {
		b, err := os.ReadFile(mf)
		if err != nil {
			return nil, err
		}
		var m swarm.Manifest
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", mf, err)
		}
		if m.Kind != swarm.ManifestKind {
			continue
		}
		if r := (&SwarmRun{Dir: filepath.Dir(mf), Manifest: m}); r.Incomplete() {
			if r.RecordedCalls, err = countCalls(filepath.Join(r.Dir, "calls.jsonl")); err != nil {
				return nil, fmt.Errorf("%s: %w", r.Dir, err)
			}
			incomplete = append(incomplete, r)
			continue
		}
		key := fmt.Sprintf("%s/%d/%v/%v", m.Arm, m.Script.Agents, m.Serial, m.ScaledServers)
		if cur, ok := latest[key]; !ok || m.Started > cur.Manifest.Started {
			latest[key] = &SwarmRun{Dir: filepath.Dir(mf), Manifest: m}
		}
	}
	var out []*SwarmRun
	for _, r := range latest {
		if r.Calls, err = corpus.ReadJSONL[swarm.Call](filepath.Join(r.Dir, "calls.jsonl")); err != nil {
			return nil, err
		}
		if r.Tasks, err = corpus.ReadJSONL[swarm.TaskResult](filepath.Join(r.Dir, "tasks.jsonl")); err != nil {
			return nil, err
		}
		if r.QuillDet, err = readQuillDet(filepath.Join(r.Dir, "quill.jsonl")); err != nil {
			return nil, fmt.Errorf("%s: %w", r.Dir, err)
		}
		out = append(out, r)
	}
	out = append(out, incomplete...)
	slices.SortFunc(out, func(a, b *SwarmRun) int {
		if c := armRank(a.Manifest.Arm) - armRank(b.Manifest.Arm); c != 0 {
			return c
		}
		if a.Manifest.Arm != b.Manifest.Arm {
			return strings.Compare(a.Manifest.Arm, b.Manifest.Arm)
		}
		if a.Manifest.Serial != b.Manifest.Serial {
			if a.Manifest.Serial {
				return 1
			}
			return -1
		}
		if c := a.Manifest.Script.Agents - b.Manifest.Script.Agents; c != 0 {
			return c
		}
		if a.Manifest.ScaledServers != b.Manifest.ScaledServers {
			if b.Manifest.ScaledServers {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Manifest.Started, b.Manifest.Started)
	})
	return out, nil
}

// countCalls counts the calls in an incomplete run's calls.jsonl, streamed
// as the run went. A last line cut short by the kill is not counted; a
// missing file counts none.
func countCalls(path string) (int, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	n, bad := 0, 0
	for line := 1; sc.Scan(); line++ {
		if bad > 0 {
			return n, fmt.Errorf("%s line %d: unreadable line before the last", path, bad)
		}
		var c struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(sc.Bytes(), &c) != nil || c.ID == "" {
			bad = line
			continue
		}
		n++
	}
	return n, sc.Err()
}

// readQuillDet reads a quill log into call ID -> whether the entry's final
// PDP decision names a determining policy (the one arms.Cedar reports).
func readQuillDet(path string) (map[string]bool, error) {
	es, err := quill.ReadAll(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(es))
	for _, e := range es {
		id, _ := e.ObservedState["record"]["id"].(string)
		det := false
		if n := len(e.PDPDecisions); n > 0 {
			det = len(e.PDPDecisions[n-1].DeterminingPolicies) > 0
		}
		out[id] = det
	}
	return out, nil
}

func armRank(a string) int {
	if i := slices.Index(armOrder, a); i >= 0 {
		return i
	}
	return len(armOrder)
}

// isBArm reports whether a is a Cedar arm (B0, G1's b, or a b- arm), which
// writes a quill; isAArm whether it is a decision-model arm. Neither decides
// verdict membership: only swarmBArms and swarmAArms enter verdicts.
func isBArm(a string) bool { return a == "b0" || a == "b" || strings.HasPrefix(a, "b-") }
func isAArm(a string) bool { return strings.HasPrefix(a, "a-") }

// registeredArm reports whether a is one of the ten registered G3 arms.
func registeredArm(a string) bool {
	return slices.Contains(swarmBArms, a) || slices.Contains(swarmAArms, a)
}

func ms(ns float64) float64 { return ns / 1e6 }

// slope is the least-squares slope of y on x, or NaN with fewer than two
// distinct x.
func slope(x, y []float64) float64 {
	n := float64(len(x))
	if len(x) < 2 {
		return math.NaN()
	}
	var sx, sy, sxx, sxy float64
	for i := range x {
		sx, sy, sxx, sxy = sx+x[i], sy+y[i], sxx+x[i]*x[i], sxy+x[i]*y[i]
	}
	d := n*sxx - sx*sx
	if d == 0 {
		return math.NaN()
	}
	return (n*sxy - sx*sy) / d
}

func fmtMS(v float64) string {
	if math.IsNaN(v) {
		return "—"
	}
	return fmt.Sprintf("%.3f", v)
}

// --- Task bootstrap ---

// Estimate is a point estimate with its 95% task-bootstrap interval.
type Estimate struct {
	Est float64 `json:"est"`
	Lo  float64 `json:"lo"`
	Hi  float64 `json:"hi"`
	// N is the sample behind the estimate: calls for a p50, tasks for a
	// slope.
	N int `json:"n"`
}

func (e Estimate) String() string {
	if math.IsNaN(e.Est) {
		return "—"
	}
	return fmt.Sprintf("%.3f [%.3f, %.3f]", e.Est, e.Lo, e.Hi)
}

func (e Estimate) json() map[string]any {
	return map[string]any{"est": num(e.Est), "lo": num(e.Lo), "hi": num(e.Hi), "n": e.N}
}

// interval sets e's bounds from resampled estimates, NaN ones dropped, as
// bounds does for rates.
func (e *Estimate) interval(est []float64) {
	est = slices.DeleteFunc(slices.Clone(est), math.IsNaN)
	if len(est) == 0 {
		e.Lo, e.Hi = e.Est, e.Est
		return
	}
	slices.Sort(est)
	e.Lo, e.Hi = est[int(0.025*float64(len(est)))], est[int(0.975*float64(len(est)-1))]
}

// nanEstimate is an estimate that could not be made.
var nanEstimate = Estimate{Est: math.NaN(), Lo: math.NaN(), Hi: math.NaN()}

// selectQ returns the p-quantile of xs as percentile defines it, reordering
// xs in place (quickselect).
func selectQ(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	k := int(math.Round(p * float64(len(xs)-1)))
	lo, hi := 0, len(xs)-1
	for lo < hi {
		pivot := xs[(lo+hi)/2]
		i, j := lo, hi
		for i <= j {
			for xs[i] < pivot {
				i++
			}
			for xs[j] > pivot {
				j--
			}
			if i <= j {
				xs[i], xs[j] = xs[j], xs[i]
				i++
				j--
			}
		}
		switch {
		case k <= j:
			hi = j
		case k >= i:
			lo = i
		default:
			return xs[k]
		}
	}
	return xs[k]
}

// gateByTask groups a run's gate latencies (ms) by task, in the run's task
// order.
func gateByTask(r *SwarmRun) [][]float64 {
	idx := make(map[string]int, len(r.Tasks))
	for i, t := range r.Tasks {
		idx[t.ID] = i
	}
	by := make([][]float64, len(r.Tasks))
	for _, c := range r.Calls {
		if i, ok := idx[c.Task]; ok {
			by[i] = append(by[i], ms(float64(c.GateNS)))
		}
	}
	return by
}

// p50Samples is a run's p50 gate latency (ms) and its value in each task
// resample (NaN where a resample has no calls).
func p50Samples(r *SwarmRun, resamples int, seed uint64) (Estimate, []float64) {
	by := gateByTask(r)
	var all []float64
	for _, b := range by {
		all = append(all, b...)
	}
	e := Estimate{Est: selectQ(slices.Clone(all), 0.5), N: len(all)}
	m := len(by)
	samples := make([]float64, resamples)
	rng := rand.New(rand.NewPCG(seed, 0x9050))
	buf := make([]float64, 0, len(all))
	for b := range samples {
		buf = buf[:0]
		for range m {
			buf = append(buf, by[rng.IntN(m)]...)
		}
		samples[b] = selectQ(buf, 0.5)
	}
	e.interval(samples)
	return e, samples
}

// taskPoint is one task's (delegation depth, critical-path ms): the depth of
// the deepest agent that ran (path.go's MaxDepth) and the critical path's
// length.
type taskPoint struct{ x, y float64 }

func taskPoints(r *SwarmRun) map[string]taskPoint {
	out := make(map[string]taskPoint, len(r.Tasks))
	for _, t := range r.Tasks {
		out[t.ID] = taskPoint{float64(t.MaxDepth), ms(float64(t.CriticalNS))}
	}
	return out
}

func slopeOf(pts []taskPoint) float64 {
	x, y := make([]float64, len(pts)), make([]float64, len(pts))
	for i, p := range pts {
		x[i], y[i] = p.x, p.y
	}
	return slope(x, y)
}

// slopeBoot is the critical-path slope on delegation depth (ms per level)
// over a run's tasks, with its task bootstrap interval.
func slopeBoot(r *SwarmRun, resamples int, seed uint64) Estimate {
	pm := taskPoints(r)
	pts := make([]taskPoint, 0, len(pm))
	for _, t := range r.Tasks {
		pts = append(pts, pm[t.ID])
	}
	e := Estimate{Est: slopeOf(pts), N: len(pts)}
	rng := rand.New(rand.NewPCG(seed, 0x51e9))
	buf := make([]taskPoint, len(pts))
	est := make([]float64, resamples)
	for b := range est {
		for i := range buf {
			buf[i] = pts[rng.IntN(len(pts))]
		}
		est[b] = slopeOf(buf)
	}
	e.interval(est)
	return e
}

// slopeDiff is the paired difference slope(a) − slope(b): both arms ran the
// same script, so each resample draws one set of tasks, by task ID, and
// computes both slopes on it.
func slopeDiff(a, b *SwarmRun, resamples int, seed uint64) Estimate {
	pa, pb := taskPoints(a), taskPoints(b)
	var ids []string
	for _, t := range a.Tasks {
		if _, ok := pb[t.ID]; ok {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		return nanEstimate
	}
	xa, xb := make([]taskPoint, len(ids)), make([]taskPoint, len(ids))
	for i, id := range ids {
		xa[i], xb[i] = pa[id], pb[id]
	}
	e := Estimate{Est: slopeOf(xa) - slopeOf(xb), N: len(ids)}
	rng := rand.New(rand.NewPCG(seed, 0xd51e))
	ba, bb := make([]taskPoint, len(ids)), make([]taskPoint, len(ids))
	est := make([]float64, resamples)
	for k := range est {
		for i := range ba {
			j := rng.IntN(len(ids))
			ba[i], bb[i] = xa[j], xb[j]
		}
		est[k] = slopeOf(ba) - slopeOf(bb)
	}
	e.interval(est)
	return e
}

// --- Verdict parts ---

// Part values.
const (
	partYes         = "yes"
	partNo          = "no"
	partNotMeasured = "not measured"
	partInstrument  = "instrument failure (re-run)"
	// partScriptDiffers: the runs a part compares ran different scripts,
	// so their tasks are not the same and cannot be paired (Law 3).
	partScriptDiffers = "not measured (script differs)"
)

// part is one registered condition's outcome, with the numbers behind it.
type part struct {
	Value  string `json:"value"`
	Detail string `json:"detail"`
}

// pairVerdict combines a pair's parts: holds iff every part is yes, fails if
// any is no, and is undetermined otherwise.
func pairVerdict(parts ...part) string {
	all := true
	for _, p := range parts {
		if p.Value == partNo {
			return "fails"
		}
		all = all && p.Value == partYes
	}
	if all {
		return "holds"
	}
	return "undetermined"
}

// --- The report ---

// BuildSwarm reports the runs as markdown and as data.
func BuildSwarm(runs []*SwarmRun, opt Options) (string, map[string]any) {
	if opt.Resamples <= 0 {
		opt.Resamples = DefaultResamples
	}
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	data := map[string]any{"resamples": opt.Resamples}
	var incomplete []*SwarmRun
	runs = slices.DeleteFunc(slices.Clone(runs), func(r *SwarmRun) bool {
		if r.Incomplete() {
			incomplete = append(incomplete, r)
			return true
		}
		return false
	})

	// A scaled-server run (G3 secondary) says so in its manifest
	// (-scaled-servers). It is kept out of the registered verdicts and
	// reported apart.
	isScaled := func(r *SwarmRun) bool { return !r.Manifest.Serial && r.Manifest.ScaledServers }
	var measured, scaled []*SwarmRun
	at := map[string]map[int]*SwarmRun{}       // primary measured runs by arm and N
	serialAt := map[string]map[int]*SwarmRun{} // serial reference runs
	for _, r := range runs {
		m := r.Manifest
		dst := at
		switch {
		case m.Serial:
			dst = serialAt
		case isScaled(r):
			scaled = append(scaled, r)
			continue
		default:
			measured = append(measured, r)
		}
		if !registeredArm(m.Arm) {
			continue // listed under other runs, never in a verdict
		}
		if dst[m.Arm] == nil {
			dst[m.Arm] = map[int]*SwarmRun{}
		}
		dst[m.Arm][m.Script.Agents] = r
	}

	w("# ambit G3 swarm report\n\n")
	w("Live agenkit swarms, every tool call gated (#21). Gate latency is the gate call's wall time as the calling agent sees it (`GateNS`), in ms: ")
	w("it includes queueing at the model server and hosted retries, and excludes the harness's in-flight wait (zero with no cap), which is shown apart. ")
	w("Intervals are 95%% bootstrap over the swarm's tasks (%d resamples); comparisons between arms are paired by task. ", opt.Resamples)
	w("Truth is the oracle's verdict on the facts each gate saw, with the semantic facts each payload was written from. ")
	w("A gate error counts as a deny. A run whose repairable (transport or capacity) errors exceed %d%% of its gate calls is an instrument failure and decides nothing. ", repairableMaxPercent)
	w("Serial runs are uncontended reference runs and carry no verdict.\n\n")

	// Runs.
	w("## Runs\n\n| arm | N | K | serial | run | git commit | script | corpus | tasks | calls | agents ran | probes ran | concurrency | instance | server slots | wall s | calls/s | swarm spend / cap | errors (repairable) | instrument | quill entries | models |\n")
	w("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	scripts := map[int]map[string]bool{}
	var runsData []map[string]any
	for _, r := range runs {
		m := r.Manifest
		dirty := ""
		if m.GitDirty {
			dirty = " (dirty)"
		}
		var models []string
		for _, k := range sortedKeys(m.Models) {
			models = append(models, k+": "+m.Models[k])
		}
		conc := "none"
		if m.Concurrency > 0 {
			conc = fmt.Sprint(m.Concurrency)
		}
		inst := "ok"
		if r.InstrumentFailure() {
			inst = "**failure: re-run**"
		}
		w("| %s | %d | %d | %s | %s | %s%s | %s | %s | %d | %d | %d | %d | %s | %s | %s | %.2f | %.0f | $%.2f / $%.2f | %d (%d) | %s | %d | %s |\n",
			m.Arm, m.Script.Agents, m.Script.Calls, map[bool]string{true: "yes", false: ""}[m.Serial], m.Run, short(m.GitCommit), dirty,
			short(m.ScriptSHA256), short(m.CorpusHash), m.Tasks, m.Gated, m.AgentsRan, m.ProbesRan, conc, orDash(m.InstanceType), slotsLabel(m.ServerSlots, isScaled(r)),
			float64(m.WallNS)/1e9, m.Throughput, float64(m.SwarmSpent)/1e6, float64(m.SwarmCap)/1e6, m.Errors, m.RepairableErrors, inst,
			m.QuillEntries, strings.Join(models, "; "))
		if !m.Serial && !isScaled(r) && registeredArm(m.Arm) {
			if scripts[m.Script.Agents] == nil {
				scripts[m.Script.Agents] = map[string]bool{}
			}
			scripts[m.Script.Agents][m.ScriptSHA256] = true
		}
		runsData = append(runsData, map[string]any{"arm": m.Arm, "agents": m.Script.Agents, "serial": m.Serial, "run": m.Run,
			"script_sha256": m.ScriptSHA256, "corpus_hash": m.CorpusHash, "git_commit": m.GitCommit, "concurrency": m.Concurrency,
			"instance_type": m.InstanceType, "wall_s": float64(m.WallNS) / 1e9, "calls_per_second": m.Throughput, "gated": m.Gated,
			"errors": m.Errors, "repairable_errors": m.RepairableErrors, "instrument_failure": r.InstrumentFailure(),
			"quill_entries": m.QuillEntries, "server_slots": m.ServerSlots, "scaled_servers": isScaled(r), "run_cap_micros": m.RunCapMicros})
	}
	data["runs"] = runsData
	data["incomplete"] = incompleteRuns(&b, incomplete)
	for _, n := range sortedKeys(scripts) {
		if len(scripts[n]) > 1 {
			w("\n**Warning:** the registered arms at N = %d ran different scripts (%d distinct hashes), so their calls differ (Law 3); paired comparisons between them are not measured.\n", n, len(scripts[n]))
		}
	}

	// Latency.
	w("\n## Gate latency by agent count\n\n| arm | N | serial | server slots | calls | p50 [95%% CI] | p95 | p99 | max | wait p50 | wait p99 |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	p50s := map[*SwarmRun]Estimate{}
	p50Draws := map[*SwarmRun][]float64{}
	var latData []map[string]any
	for _, r := range runs {
		m := r.Manifest
		g := make([]float64, len(r.Calls))
		wt := make([]float64, len(r.Calls))
		for i, c := range r.Calls {
			g[i], wt[i] = ms(float64(c.GateNS)), ms(float64(c.WaitNS))
		}
		e, draws := p50Samples(r, opt.Resamples, seedFor("swarm-p50", m.Arm, fmt.Sprint(m.Script.Agents), fmt.Sprint(m.Serial), fmt.Sprint(m.ServerSlots)))
		p50s[r], p50Draws[r] = e, draws
		w("| %s | %d | %s | %s | %d | %s | %s | %s | %s | %s | %s |\n", m.Arm, m.Script.Agents, map[bool]string{true: "yes", false: ""}[m.Serial],
			slotsLabel(m.ServerSlots, isScaled(r)), len(r.Calls),
			e, fmtMS(percentile(g, 0.95)), fmtMS(percentile(g, 0.99)), fmtMS(percentile(g, 1)), fmtMS(percentile(wt, 0.5)), fmtMS(percentile(wt, 0.99)))
		latData = append(latData, map[string]any{"arm": m.Arm, "agents": m.Script.Agents, "serial": m.Serial, "server_slots": m.ServerSlots, "scaled_servers": isScaled(r), "p50_ms": e.json(),
			"p95_ms": num(percentile(g, 0.95)), "p99_ms": num(percentile(g, 0.99))})
	}
	data["gate_latency"] = latData

	// Critical path by depth.
	w("\n## Critical path by delegation depth\n\n")
	w("A task's critical path is the chain of gated calls with the latest finish on its dependency graph: delegations from the root down to one agent, then that agent's work calls. ")
	w("A task's delegation depth is the depth of the deepest agent that ran. The last columns split the path's latency by the depth of the calling agent (means over the row's tasks).\n\n")
	w("| arm | N | task depth | tasks | critical path p50 | p95 | mean | path calls (mean) |")
	for d := 0; d <= vocab.MaxDepth+1; d++ {
		w(" at depth %d |", d)
	}
	w("\n|---|---|---|---|---|---|---|---|%s\n", strings.Repeat("---|", vocab.MaxDepth+2))
	var cpData []map[string]any
	for _, r := range measured {
		for d := 0; d <= vocab.MaxDepth+1; d++ {
			var cps, calls []float64
			by := make([]float64, vocab.MaxDepth+2)
			for _, t := range r.Tasks {
				if t.MaxDepth != d {
					continue
				}
				cps = append(cps, float64(t.CriticalNS))
				calls = append(calls, float64(t.CriticalCalls))
				for i, v := range t.ByDepthNS {
					if i < len(by) {
						by[i] += float64(v)
					}
				}
			}
			if len(cps) == 0 {
				continue
			}
			w("| %s | %d | %d | %d | %s | %s | %s | %.1f |", r.Manifest.Arm, r.Manifest.Script.Agents, d, len(cps),
				fmtMS(ms(percentile(cps, 0.5))), fmtMS(ms(percentile(cps, 0.95))), fmtMS(ms(mean(cps))), mean(calls))
			split := make([]any, len(by))
			for i := range by {
				w(" %s |", fmtMS(ms(by[i]/float64(len(cps)))))
				split[i] = num(ms(by[i] / float64(len(cps))))
			}
			w("\n")
			cpData = append(cpData, map[string]any{"arm": r.Manifest.Arm, "agents": r.Manifest.Script.Agents, "task_depth": d,
				"tasks": len(cps), "p50_ms": num(ms(percentile(cps, 0.5))), "mean_ms": num(ms(mean(cps))), "by_depth_mean_ms": split})
		}
	}
	data["critical_path"] = cpData

	// H-scale.
	hData := hScale(&b, at, serialAt, p50s, p50Draws, opt.Resamples)
	data["h_scale"] = hData
	data["scaled_servers"] = scaledServers(&b, scaled, at, p50s, opt.Resamples)
	data["other_runs"] = otherRuns(&b, runs, p50s, opt.Resamples)

	// Reported, no verdict.
	data["correctness"] = swarmCorrectness(&b, measured, opt.Resamples)
	data["d3_transferred_threshold"] = d3Transfer(&b, measured, opt.Resamples)
	data["depth_breaches"], data["cap_overshoot"] = breaches(&b, measured)
	data["cost"] = swarmCost(&b, runs)
	data["auditability"] = swarmAudit(&b, measured, opt.Resamples)
	return b.String(), data
}

// hScale applies the registered decision rules and writes the H-scale
// section.
func hScale(b *strings.Builder, at, serialAt map[string]map[int]*SwarmRun, p50s map[*SwarmRun]Estimate,
	p50Draws map[*SwarmRun][]float64, resamples int) map[string]any {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	// Only the registered arms enter verdicts (otherRuns lists the rest).
	bArms, aArms := swarmBArms, swarmAArms
	failed := func(rs ...*SwarmRun) bool {
		for _, r := range rs {
			if r.InstrumentFailure() {
				return true
			}
		}
		return false
	}

	w("\n## H-scale\n\n")
	w("Registered (#21): B flat in agent count (95%% upper bound of p50(N = %d) / p50(N = %d) ≤ %.0f); ", hScaleTopN, hScaleBaseN, hScaleFlatMax)
	w("A compounds with delegation depth (at N = %d, the 95%% lower bound of the slope of task critical path on delegation depth > 0, and of the paired difference to every B arm's slope > 0); ", hScaleN)
	w("at N = %d, B p50 95%% upper bound < %.0f ms and A p50 95%% lower bound > %.0f ms. H-scale holds for a pair iff all four parts hold.\n\n", hScaleN, hScaleBMaxMS, hScaleAMinMS)

	// Probe resolution (Law 5): the serial reference p50 at N = 100.
	serialNote := map[string]string{}
	serialData := map[string]any{}
	for _, a := range append(slices.Clone(bArms), aArms...) {
		r := serialAt[a][hScaleBaseN]
		if r == nil {
			continue
		}
		e := p50s[r]
		serialData[a] = e.json()
		switch {
		case slices.Contains(bArms, a) && e.Est >= hScaleBMaxMS:
			serialNote[a] = fmt.Sprintf("serial reference p50 at N = %d is already %.3f ms, not under %.0f ms: a property of the system, not of scale", hScaleBaseN, e.Est, hScaleBMaxMS)
		case slices.Contains(aArms, a) && !(e.Est > hScaleAMinMS):
			serialNote[a] = fmt.Sprintf("serial reference p50 at N = %d is already %.3f ms, not above %.0f ms: a property of the system, not of scale", hScaleBaseN, e.Est, hScaleAMinMS)
		}
	}

	// 1. B flat.
	w("### B flat in agent count\n\n| arm | p50 at N = %d | p50 at N = %d | r = ratio [95%% CI] | flat (upper bound ≤ %.0f) |\n|---|---|---|---|---|\n", hScaleBaseN, hScaleTopN, hScaleFlatMax)
	flat := map[string]part{}
	var flatData []map[string]any
	for _, a := range bArms {
		lo, hi := at[a][hScaleBaseN], at[a][hScaleTopN]
		p := part{Value: partNotMeasured}
		ratio := nanEstimate
		switch {
		case lo == nil || hi == nil:
			p.Detail = fmt.Sprintf("needs runs at N = %d and N = %d", hScaleBaseN, hScaleTopN)
		default:
			el, eh := p50s[lo], p50s[hi]
			ratio = Estimate{Est: eh.Est / el.Est, N: eh.N}
			dl, dh := p50Draws[lo], p50Draws[hi]
			draws := make([]float64, len(dl))
			for i := range dl {
				draws[i] = dh[i] / dl[i]
			}
			ratio.interval(draws)
			p.Detail = "r = " + ratio.String()
			switch {
			case failed(lo, hi):
				p.Value = partInstrument
			case math.IsNaN(ratio.Hi):
				p.Value = partNotMeasured
			default:
				p.Value = yesNo(ratio.Hi <= hScaleFlatMax)
			}
		}
		flat[a] = p
		pl, ph := nanEstimate, nanEstimate
		if lo != nil {
			pl = p50s[lo]
		}
		if hi != nil {
			ph = p50s[hi]
		}
		w("| %s | %s | %s | %s | **%s** |\n", a, pl, ph, ratio, p.Value)
		flatData = append(flatData, map[string]any{"arm": a, "p50_base_ms": pl.json(), "p50_top_ms": ph.json(), "ratio": ratio.json(), "flat": p})
	}

	// 2. A compounds.
	slopes := map[string]Estimate{}
	for _, a := range append(slices.Clone(bArms), aArms...) {
		if r := at[a][hScaleN]; r != nil {
			slopes[a] = slopeBoot(r, resamples, seedFor("swarm-slope", a))
		}
	}
	w("\n### A compounds with delegation depth (N = %d)\n\n", hScaleN)
	w("Least-squares slope of a task's critical-path latency (ms) on its delegation depth, ms per level, with the paired (by task) difference A − B for every B arm.\n\n")
	w("| arm | slope [95%% CI] |")
	for _, ba := range bArms {
		w(" A − %s |", ba)
	}
	w(" compounds |\n|---|---|%s---|\n", strings.Repeat("---|", len(bArms)))
	for _, a := range bArms {
		e, ok := slopes[a]
		if !ok {
			e = nanEstimate
		}
		w("| %s | %s |%s — |\n", a, e, strings.Repeat(" |", len(bArms)))
	}
	compounds := map[string]part{}
	var compData []map[string]any
	for _, a := range aArms {
		ra := at[a][hScaleN]
		p := part{Value: partNotMeasured, Detail: fmt.Sprintf("needs a run at N = %d", hScaleN)}
		e := nanEstimate
		diffs := map[string]any{}
		var cells []string
		if ra != nil {
			e = slopes[a]
			p.Detail = "slope " + e.String()
			anyNo, missing, instr, differs := !(e.Lo > 0), false, failed(ra), false
			if math.IsNaN(e.Lo) {
				anyNo, missing = false, true
			}
			for _, ba := range bArms {
				rb := at[ba][hScaleN]
				if rb == nil {
					missing = true
					cells = append(cells, "not measured")
					diffs[ba] = nil
					continue
				}
				instr = instr || failed(rb)
				if rb.Manifest.ScriptSHA256 != ra.Manifest.ScriptSHA256 {
					// Coincident task IDs of different scripts are not
					// the same tasks: no paired difference.
					differs = true
					cells = append(cells, partScriptDiffers)
					diffs[ba] = partScriptDiffers
					continue
				}
				d := slopeDiff(ra, rb, resamples, seedFor("swarm-slope-diff", a, ba))
				cells = append(cells, d.String())
				diffs[ba] = d.json()
				switch {
				case math.IsNaN(d.Lo):
					missing = true
				case !(d.Lo > 0):
					anyNo = true
				}
			}
			switch {
			case instr:
				p.Value = partInstrument
			case anyNo:
				p.Value = partNo
			case differs:
				p.Value = partScriptDiffers
				p.Detail += "; a B arm at N = 1,000 ran a different script"
			case missing:
				p.Value = partNotMeasured
				p.Detail += "; not every B arm measured at N = 1,000"
			default:
				p.Value = partYes
			}
		} else {
			for range bArms {
				cells = append(cells, "—")
			}
		}
		compounds[a] = p
		w("| %s | %s | %s | **%s** |\n", a, e, strings.Join(cells, " | "), p.Value)
		compData = append(compData, map[string]any{"arm": a, "slope_ms_per_level": e.json(), "paired_difference_to_b": diffs, "compounds": p})
	}
	var bSlopeData []map[string]any
	for _, a := range bArms {
		if e, ok := slopes[a]; ok {
			bSlopeData = append(bSlopeData, map[string]any{"arm": a, "slope_ms_per_level": e.json()})
		}
	}

	// 3. p50 at N = 1,000.
	w("\n### p50 at N = %d\n\n| arm | p50 [95%% CI] | threshold | meets | serial reference p50 at N = %d | probe resolution |\n|---|---|---|---|---|---|\n", hScaleN, hScaleBaseN)
	meets := map[string]part{}
	var meetData []map[string]any
	for _, a := range append(slices.Clone(bArms), aArms...) {
		r := at[a][hScaleN]
		e := nanEstimate
		p := part{Value: partNotMeasured, Detail: fmt.Sprintf("needs a run at N = %d", hScaleN)}
		threshold := fmt.Sprintf("upper bound < %.0f ms", hScaleBMaxMS)
		if slices.Contains(aArms, a) {
			threshold = fmt.Sprintf("lower bound > %.0f ms", hScaleAMinMS)
		}
		if r != nil {
			e = p50s[r]
			p.Detail = "p50 " + e.String() + " ms"
			switch {
			case failed(r):
				p.Value = partInstrument
			case slices.Contains(bArms, a):
				p.Value = yesNo(e.Hi < hScaleBMaxMS)
			default:
				p.Value = yesNo(e.Lo > hScaleAMinMS)
			}
		}
		meets[a] = p
		ser := "—"
		if sr := serialAt[a][hScaleBaseN]; sr != nil {
			ser = p50s[sr].String()
		}
		w("| %s | %s | %s | **%s** | %s | %s |\n", a, e, threshold, p.Value, ser, orDash(serialNote[a]))
		meetData = append(meetData, map[string]any{"arm": a, "p50_ms": e.json(), "meets": p, "probe_resolution": serialNote[a]})
	}

	// 4. Pairs.
	w("\n### H-scale by pair\n\nEach part is given separately; the pair holds iff all four are yes.\n\n")
	w("| B arm | A arm | B flat | B p50 < %.0f ms | A compounds | A p50 > %.0f ms | H-scale | notes |\n|---|---|---|---|---|---|---|---|\n", hScaleBMaxMS, hScaleAMinMS)
	var pairData []map[string]any
	counts := map[string]int{}
	for _, ba := range bArms {
		for _, aa := range aArms {
			v := pairVerdict(flat[ba], meets[ba], compounds[aa], meets[aa])
			counts[v]++
			var notes []string
			for _, a := range []string{ba, aa} {
				if n := serialNote[a]; n != "" {
					notes = append(notes, a+": "+n)
				}
			}
			w("| %s | %s | %s | %s | %s | %s | **%s** | %s |\n", ba, aa, flat[ba].Value, meets[ba].Value, compounds[aa].Value, meets[aa].Value, v,
				orDash(strings.Join(notes, "; ")))
			pairData = append(pairData, map[string]any{"b": ba, "a": aa, "b_flat": flat[ba], "b_p50": meets[ba], "a_compounds": compounds[aa],
				"a_p50": meets[aa], "h_scale": v, "notes": notes})
		}
	}
	w("\n- **H-scale**: %d of %d pairs hold, %d fail, %d undetermined (parts not measured or instrument failures). Pair verdicts are never read alone; see the parts above.\n",
		counts["holds"], len(bArms)*len(aArms), counts["fails"], counts["undetermined"])
	return map[string]any{"b_flat": flatData, "a_compounds": compData, "b_slopes": bSlopeData, "p50_at_1000": meetData,
		"serial_reference_p50_at_100": serialData, "pairs": pairData, "pair_counts": counts}
}

// swarmCorrectness writes accuracy, false-allow and false-deny against the
// oracle per arm and N, overall and by policy, ambit class, and call kind.
func swarmCorrectness(b *strings.Builder, runs []*SwarmRun, resamples int) []map[string]any {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	type split struct {
		name string
		keep func(c *swarm.Call) bool
	}
	var byPolicy, byClass, byKind []split
	for _, p := range sortedKeys(policyNames) {
		byPolicy = append(byPolicy, split{policyNames[p], func(c *swarm.Call) bool { return c.Truth.Policy == p }})
	}
	for _, cl := range []string{corpus.ClassWithin, corpus.ClassMargin, corpus.ClassOutside} {
		byClass = append(byClass, split{cl, func(c *swarm.Call) bool { return c.Truth.Class == cl }})
	}
	byKind = []split{
		{"work", func(c *swarm.Call) bool { return c.Kind == swarm.KindWork && !c.Probe }},
		{"delegate", func(c *swarm.Call) bool { return c.Kind == swarm.KindDelegate && !c.Probe }},
		{"probe agent's calls", func(c *swarm.Call) bool { return c.Probe }},
	}
	var out []map[string]any
	w("\n## Correctness against the oracle (reported, no verdict)\n\n| arm | N | accuracy | false-allow (of oracle denies) | false-deny (of oracle allows) | errors |\n|---|---|---|---|---|---|\n")
	for _, r := range runs {
		acc, fa, fd := swarmRates(r, nil, resamples, func(c *swarm.Call) bool { return c.Decision.Allow })
		w("| %s | %d | %s | %s | %s | %d |\n", r.Manifest.Arm, r.Manifest.Script.Agents, acc, fa, fd, r.Manifest.Errors)
		out = append(out, map[string]any{"arm": r.Manifest.Arm, "agents": r.Manifest.Script.Agents, "accuracy": acc, "false_allow": fa, "false_deny": fd})
	}
	for _, sp := range []struct {
		title  string
		splits []split
	}{{"policy", byPolicy}, {"ambit class", byClass}, {"call kind", byKind}} {
		w("\n### By %s\n\n| arm | N | %s | accuracy | false-allow | false-deny |\n|---|---|---|---|---|---|\n", sp.title, sp.title)
		for _, r := range runs {
			for _, s := range sp.splits {
				acc, fa, fd := swarmRates(r, s.keep, resamples, func(c *swarm.Call) bool { return c.Decision.Allow })
				if fa.N+fd.N == 0 {
					continue
				}
				w("| %s | %d | %s | %s | %s | %s |\n", r.Manifest.Arm, r.Manifest.Script.Agents, s.name, acc, fa, fd)
			}
		}
	}
	return out
}

// swarmRates is accuracy, false-allow and false-deny over a run's kept
// calls, with allow deciding each call, bootstrapped over tasks.
func swarmRates(r *SwarmRun, keep func(c *swarm.Call) bool, resamples int, allow func(c *swarm.Call) bool) (acc, fa, fd Interval) {
	fams := taskFamilies(r)
	s := newScorer(len(fams), resamples)
	var all, deny, ok []item
	for i := range r.Calls {
		c := &r.Calls[i]
		if keep != nil && !keep(c) {
			continue
		}
		f := fams[c.Task]
		a := allow(c)
		all = append(all, item{f, a == (c.Truth.Verdict == corpus.Allow)})
		if c.Truth.Verdict == corpus.Deny {
			deny = append(deny, item{f, a})
		} else {
			ok = append(ok, item{f, !a})
		}
	}
	key := fmt.Sprintf("%s/%d", r.Manifest.Arm, r.Manifest.Script.Agents)
	return s.rate(all, seedFor("swarm", key, "acc")), s.rate(deny, seedFor("swarm", key, "fa")), s.rate(ok, seedFor("swarm", key, "fd"))
}

// taskFamilies indexes a run's tasks, in sorted order, as bootstrap
// families. Calls of tasks the run did not finish share one index.
func taskFamilies(r *SwarmRun) map[string]int32 {
	ids := make([]string, 0, len(r.Tasks))
	for _, t := range r.Tasks {
		ids = append(ids, t.ID)
	}
	for _, c := range r.Calls {
		ids = append(ids, c.Task)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	fams := make(map[string]int32, len(ids))
	for i, id := range ids {
		fams[id] = int32(i)
	}
	return fams
}

// d3Transfer recomputes A-d3's accuracy at the G2b-transferred threshold
// from the recorded P (exploratory, #21): the live gate decided at 0.5.
func d3Transfer(b *strings.Builder, runs []*SwarmRun, resamples int) []map[string]any {
	var out []map[string]any
	var rows []string
	for _, r := range runs {
		if r.Manifest.Arm != "a-d3" {
			continue
		}
		acc, fa, fd := swarmRates(r, nil, resamples, func(c *swarm.Call) bool {
			d := c.Decision
			return d.Error == "" && !d.Refused && d.PAllow != nil && *d.PAllow >= d3TransferThreshold
		})
		rows = append(rows, fmt.Sprintf("| %s | %d | %s | %s | %s |\n", r.Manifest.Arm, r.Manifest.Script.Agents, acc, fa, fd))
		out = append(out, map[string]any{"arm": r.Manifest.Arm, "agents": r.Manifest.Script.Agents, "threshold": d3TransferThreshold,
			"accuracy": acc, "false_allow": fa, "false_deny": fd})
	}
	if len(rows) > 0 {
		fmt.Fprintf(b, "\n### A-d3 at the G2b-transferred threshold %.4g (exploratory)\n\nRecomputed per call from the recorded P; the live gate decided at 0.5, so later calls followed its as-run decisions.\n\n", d3TransferThreshold)
		fmt.Fprintf(b, "| arm | N | accuracy | false-allow | false-deny |\n|---|---|---|---|---|\n%s", strings.Join(rows, ""))
	}
	return out
}

// breaches writes the depth breaches (probes that ran) and the live cap
// overshoot per run.
func breaches(b *strings.Builder, runs []*SwarmRun) ([]map[string]any, []map[string]any) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w("\n## Depth breaches and live cap overshoot (reported, no verdict)\n\n")
	w("A probe is a sub-agent one level below the depth limit; it runs only if a gate allows the delegation that creates it. ")
	w("Cap overshoot is spend beyond a cap, possible when concurrent calls each saw headroom only one could use.\n\n")
	w("| arm | N | probe delegations asked | probes ran | swarm spend / cap | swarm overshoot | tasks over cap | task overshoot (sum) |\n|---|---|---|---|---|---|---|---|\n")
	var depth, caps []map[string]any
	for _, r := range runs {
		m := r.Manifest
		asked := 0
		for _, c := range r.Calls {
			if c.Kind == swarm.KindDelegate && c.Depth == vocab.MaxDepth {
				asked++
			}
		}
		over := max(0, m.SwarmSpent-m.SwarmCap)
		tasksOver := 0
		var taskOver int64
		for _, t := range r.Tasks {
			if t.TaskSpent > t.TaskCap {
				tasksOver++
				taskOver += t.TaskSpent - t.TaskCap
			}
		}
		w("| %s | %d | %d | %d | $%.2f / $%.2f | $%.2f | %d of %d | $%.2f |\n", m.Arm, m.Script.Agents, asked, m.ProbesRan,
			float64(m.SwarmSpent)/1e6, float64(m.SwarmCap)/1e6, float64(over)/1e6, tasksOver, len(r.Tasks), float64(taskOver)/1e6)
		depth = append(depth, map[string]any{"arm": m.Arm, "agents": m.Script.Agents, "probe_delegations": asked, "probes_ran": m.ProbesRan})
		caps = append(caps, map[string]any{"arm": m.Arm, "agents": m.Script.Agents, "swarm_overshoot_micros": over,
			"tasks_over_cap": tasksOver, "task_overshoot_micros": taskOver})
	}
	return depth, caps
}

// swarmCost writes each run's cost and its cost per 1k gate calls: hosted
// spend from the ledger, other model spend (Bedrock) at list price from the
// decisions, and GPU time at on-demand list price for the run's wall time
// on its instance (the compute-cost method; setup is not counted).
func swarmCost(b *strings.Builder, runs []*SwarmRun) []map[string]any {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w("\n## Cost (reported, no verdict)\n\n")
	w("Hosted spend is the ledger's; Bedrock is at list price; GPU time is the run's wall time at the on-demand list price of its instance type (setup not counted). ")
	w("Runs off EC2 have no GPU cost.\n\n")
	w("| arm | N | serial | gate calls | model $ | source | instance | GPU $ | total $ | $ per 1k gate calls |\n|---|---|---|---|---|---|---|---|---|---|\n")
	var out []map[string]any
	for _, r := range runs {
		m := r.Manifest
		model, src := m.ModelCostMicros, "decisions (list price)"
		if _, ok := m.Models["hosted"]; ok {
			model, src = m.SpendMicros, "ledger"
		}
		if model == 0 && src != "ledger" {
			src = "none"
		}
		gpu, gpuS := 0.0, "—"
		if m.InstanceType != "" {
			if p, ok := onDemandUSDPerHour[m.InstanceType]; ok {
				gpu = p * float64(m.WallNS) / 3.6e12
				gpuS = fmt.Sprintf("%.4f", gpu)
			} else {
				gpuS = "unpriced"
			}
		}
		total := float64(model)/1e6 + gpu
		per1k := math.NaN()
		if m.Gated > 0 {
			per1k = total / float64(m.Gated) * 1000
		}
		w("| %s | %d | %s | %d | %.4f | %s | %s | %s | %.4f | %s |\n", m.Arm, m.Script.Agents, map[bool]string{true: "yes", false: ""}[m.Serial],
			m.Gated, float64(model)/1e6, src, orDash(m.InstanceType), gpuS, total, fmtMS(per1k))
		out = append(out, map[string]any{"arm": m.Arm, "agents": m.Script.Agents, "serial": m.Serial, "model_usd": float64(model) / 1e6,
			"model_source": src, "instance_type": m.InstanceType, "gpu_usd": gpu, "total_usd": total, "usd_per_1k_calls": num(per1k)})
	}
	return out
}

// swarmAudit writes the Cedar arms' auditability, as H-audit does for G2:
// the share of denials whose quill entry names a determining policy,
// excluding outside-ambit default denials (which carry the reason "no
// policy permits this action"). A denial with no quill entry counts as
// naming none.
func swarmAudit(b *strings.Builder, runs []*SwarmRun, resamples int) []map[string]any {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w("\n## Auditability (reported, no verdict)\n\n")
	w("Cedar arms (B0 and the B arms) write one LKI quill entry per gate decision (quill.jsonl). The share is of denials whose quill entry names a determining policy; ")
	w("the last column excludes outside-ambit default denials, as G2's H-audit does. Arm A names no policy.\n\n")
	w("| arm | N | quill entries / gate calls | denials with a determining policy | same, excluding outside-ambit default denials | quill disagrees with decision |\n|---|---|---|---|---|---|\n")
	var out []map[string]any
	for _, r := range runs {
		m := r.Manifest
		if !isBArm(m.Arm) {
			continue
		}
		fams := taskFamilies(r)
		s := newScorer(len(fams), resamples)
		var all, in []item
		disagree := 0
		for i := range r.Calls {
			c := &r.Calls[i]
			det, ok := r.QuillDet[c.ID]
			if !ok || det != (len(c.Decision.Determining) > 0) {
				disagree++
			}
			if c.Decision.Allow {
				continue
			}
			it := item{fams[c.Task], det}
			all = append(all, it)
			if c.Truth.Class != corpus.ClassOutside {
				in = append(in, it)
			}
		}
		key := fmt.Sprintf("%s/%d", m.Arm, m.Script.Agents)
		ra, ri := s.rate(all, seedFor("swarm-audit", key)), s.rate(in, seedFor("swarm-audit-in", key))
		w("| %s | %d | %d / %d | %s | %s | %d |\n", m.Arm, m.Script.Agents, len(r.QuillDet), len(r.Calls), ra, ri, disagree)
		out = append(out, map[string]any{"arm": m.Arm, "agents": m.Script.Agents, "quill_entries": len(r.QuillDet), "gate_calls": len(r.Calls),
			"audit": ra, "audit_excluding_outside": ri, "quill_disagrees": disagree})
	}
	return out
}

// scaledServers writes the G3 secondary: runs whose model server had more
// parallel slots than the arm's primary runs, next to the primary run at the
// same N. They decide no registered verdict.
func scaledServers(b *strings.Builder, scaled []*SwarmRun, at map[string]map[int]*SwarmRun, p50s map[*SwarmRun]Estimate, resamples int) []map[string]any {
	if len(scaled) == 0 {
		return nil
	}
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w("\n## Scaled servers (secondary)\n\n")
	w("Runs with the model server at more parallel slots than the arm's primary runs (G3 secondary: 4x the G2 slots), beside the primary run at the same N. ")
	w("They are outside the registered H-scale verdicts.\n\n")
	w("| arm | N | server slots | p50 [95%% CI] | critical-path slope ms/level [95%% CI] | instrument | primary slots | primary p50 | primary slope |\n|---|---|---|---|---|---|---|---|---|\n")
	var out []map[string]any
	for _, r := range scaled {
		m := r.Manifest
		sl := slopeBoot(r, resamples, seedFor("swarm-slope", m.Arm, "scaled", fmt.Sprint(m.ServerSlots)))
		pp, ps, pslots := nanEstimate, nanEstimate, "—"
		if pr := at[m.Arm][m.Script.Agents]; pr != nil {
			pp, ps = p50s[pr], slopeBoot(pr, resamples, seedFor("swarm-slope", m.Arm))
			pslots = slotsLabel(pr.Manifest.ServerSlots, false)
		}
		inst := "ok"
		if r.InstrumentFailure() {
			inst = "failure: re-run"
		}
		w("| %s | %d | %d | %s | %s | %s | %s | %s | %s |\n", m.Arm, m.Script.Agents, m.ServerSlots, p50s[r], sl, inst, pslots, pp, ps)
		out = append(out, map[string]any{"arm": m.Arm, "agents": m.Script.Agents, "server_slots": m.ServerSlots, "p50_ms": p50s[r].json(),
			"slope_ms_per_level": sl.json(), "primary_p50_ms": pp.json(), "primary_slope_ms_per_level": ps.json(), "instrument_failure": r.InstrumentFailure()})
	}
	return out
}

func slotsLabel(n int, scaled bool) string {
	switch {
	case n == 0:
		return "—"
	case scaled:
		return fmt.Sprintf("%d (scaled)", n)
	}
	return fmt.Sprint(n)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func sortedKeys[K int | string, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// incompleteRuns lists the runs that did not finish: killed (manifest
// unfinished), stopped early, or still running. They enter no verdict and
// no other table; the calls they streamed are counted.
func incompleteRuns(b *strings.Builder, runs []*SwarmRun) []map[string]any {
	if len(runs) == 0 {
		return nil
	}
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w("\n## Incomplete runs (no verdict)\n\n")
	w("Runs that did not finish: stopped early, or with an unfinished manifest (killed, or still running). Their calls were streamed to calls.jsonl as they ran; they decide nothing.\n\n")
	w("| arm | N | serial | run | started | status | calls recorded | scripted calls | script |\n|---|---|---|---|---|---|---|---|---|\n")
	var out []map[string]any
	for _, r := range runs {
		m := r.Manifest
		status := "stopped: " + m.Stopped
		if m.Stopped == "" {
			status = "unfinished (no end recorded: killed or running)"
		}
		w("| %s | %d | %s | %s | %s | %s | %d | %d | %s |\n", m.Arm, m.Script.Agents, map[bool]string{true: "yes", false: ""}[m.Serial],
			m.Run, m.Started, status, r.RecordedCalls, m.ScriptedCalls, short(m.ScriptSHA256))
		out = append(out, map[string]any{"arm": m.Arm, "agents": m.Script.Agents, "serial": m.Serial, "run": m.Run, "started": m.Started,
			"stopped": m.Stopped, "finished": m.Finished, "calls_recorded": r.RecordedCalls, "scripted_calls": m.ScriptedCalls,
			"script_sha256": m.ScriptSHA256})
	}
	return out
}

// otherRuns lists complete runs of arms outside the ten registered G3 arms
// (G1's b, ablations, -name runs). They enter no verdict.
func otherRuns(b *strings.Builder, runs []*SwarmRun, p50s map[*SwarmRun]Estimate, resamples int) []map[string]any {
	var others []*SwarmRun
	for _, r := range runs {
		if !registeredArm(r.Manifest.Arm) {
			others = append(others, r)
		}
	}
	if len(others) == 0 {
		return nil
	}
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w("\n## Other runs (no verdict)\n\n")
	w("Runs of arms outside the ten registered G3 arms (%s; %s). They enter no H-scale part or pair.\n\n",
		strings.Join(swarmBArms, ", "), strings.Join(swarmAArms, ", "))
	w("| arm | N | serial | calls | p50 [95%% CI] | critical-path slope ms/level [95%% CI] | script |\n|---|---|---|---|---|---|---|\n")
	var out []map[string]any
	for _, r := range others {
		m := r.Manifest
		sl := slopeBoot(r, resamples, seedFor("swarm-slope", m.Arm, "other", fmt.Sprint(m.Script.Agents)))
		w("| %s | %d | %s | %d | %s | %s | %s |\n", m.Arm, m.Script.Agents, map[bool]string{true: "yes", false: ""}[m.Serial],
			len(r.Calls), p50s[r], sl, short(m.ScriptSHA256))
		out = append(out, map[string]any{"arm": m.Arm, "agents": m.Script.Agents, "serial": m.Serial, "p50_ms": p50s[r].json(),
			"slope_ms_per_level": sl.json(), "script_sha256": m.ScriptSHA256})
	}
	return out
}
