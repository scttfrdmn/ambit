package report

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttfrdmn/ambit/internal/corpus"
)

func TestPairedIsDiffPlusRatio(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	var a, b []item
	for f := 0; f < 50; f++ {
		for k := 0; k < 8; k++ {
			a = append(a, item{fam: int32(f), hit: r.IntN(10) == 0})
			b = append(b, item{fam: int32(f), hit: r.IntN(2) == 0})
		}
	}
	s := newScorer(50, 500)
	d := s.diff(a, b, 9)
	ps := s.paired(a, b, 9)
	if ps.Diff != d {
		t.Fatalf("paired difference %+v, diff %+v", ps.Diff, d)
	}
	if ps.Ratio.Resamples != 500 || ps.Ratio.Excluded != 0 || ps.Ratio.Lo > ps.Ratio.Est || ps.Ratio.Hi < ps.Ratio.Est {
		t.Fatalf("ratio %+v", ps.Ratio)
	}
	// B is far below A: every resample has B − A < 0, so p is 1/(R+1).
	if ps.P != 1.0/501 {
		t.Fatalf("p %v", ps.P)
	}
}

func TestRatioInterval(t *testing.T) {
	s := newScorer(20, 400)
	// Every family has B at 1/10 and A at 5/10: the ratio is 0.2 in every
	// resample.
	var b, a []item
	for f := 0; f < 20; f++ {
		for k := 0; k < 10; k++ {
			b = append(b, item{fam: int32(f), hit: k < 1})
			a = append(a, item{fam: int32(f), hit: k < 5})
		}
	}
	ps := s.paired(b, a, 1)
	if math.Abs(ps.Ratio.Est-0.2) > 1e-12 || math.Abs(ps.Ratio.Lo-0.2) > 1e-12 || math.Abs(ps.Ratio.Hi-0.2) > 1e-12 || !ps.Ratio.UpperAtMost(0.2) || ps.Ratio.UpperAtMost(0.1) {
		t.Fatalf("ratio %+v", ps.Ratio)
	}

	// Neither arm has a false allow: 0/0 everywhere, every resample excluded.
	var z []item
	for f := 0; f < 20; f++ {
		z = append(z, item{fam: int32(f)})
	}
	ps = s.paired(z, z, 1)
	if !math.IsNaN(ps.Ratio.Est) || ps.Ratio.Excluded != 400 || ps.Ratio.Resamples != 0 || ps.Ratio.UpperAtMost(0.1) {
		t.Fatalf("0/0 ratio %+v", ps.Ratio)
	}
	if !strings.Contains(ps.Ratio.String(), "A made no false allows") {
		t.Fatalf("0/0 ratio renders %q", ps.Ratio)
	}

	// B's false allows are in family 0 and A's in family 1 only: resamples
	// without family 1 give ∞ (with family 0) or 0/0 (without it).
	b, a = nil, nil
	for f := 0; f < 20; f++ {
		b = append(b, item{fam: int32(f), hit: f == 0})
		a = append(a, item{fam: int32(f), hit: f == 1})
	}
	ps = s.paired(b, a, 2)
	if ps.Ratio.Est != 1 || !math.IsInf(ps.Ratio.Hi, 1) || ps.Ratio.Excluded == 0 || ps.Ratio.Excluded+ps.Ratio.Resamples != 400 {
		t.Fatalf("ratio with zero resamples %+v", ps.Ratio)
	}
	if _, err := json.Marshal(ps.Ratio); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ps.Ratio.String(), "∞") || !strings.Contains(ps.Ratio.String(), "excluded") {
		t.Fatalf("renders %q", ps.Ratio)
	}
}

func TestHolm(t *testing.T) {
	// Sorted: 0.005×4 = 0.02; 0.01×3 = 0.03; 0.03×2 = 0.06; 0.04×1 = 0.04,
	// raised to 0.06 by the step-down maximum.
	got := holm([]float64{0.01, 0.04, 0.03, 0.005})
	want := []float64{0.03, 0.06, 0.06, 0.02}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("holm %v, want %v", got, want)
		}
	}
	if got := holm([]float64{0.4, 0.5}); got[0] != 0.8 || got[1] != 0.8 {
		t.Fatalf("holm %v", got)
	}
	if got := holm([]float64{0.9, 0.7}); got[0] != 1 || got[1] != 1 {
		t.Fatalf("holm caps at 1: %v", got)
	}
}

func TestCalibrate(t *testing.T) {
	cal := calibrate([]float64{0.05, 0.15, 0.95, 0.95, 1}, []bool{false, true, true, false, true})
	// Brier: (0.0025 + 0.7225 + 0.0025 + 0.9025 + 0) / 5.
	if math.Abs(cal.Brier-1.63/5) > 1e-12 {
		t.Fatalf("brier %v", cal.Brier)
	}
	// Bins: [0, 0.1) 0.05 vs 0; [0.1, 0.2) 0.15 vs 1; [0.9, 1] mean
	// 0.9667 vs 2/3. ECE = (0.05 + 0.85 + 3 × 0.3) / 5.
	if math.Abs(cal.ECE-(0.05+0.85+3*(2.9/3-2.0/3))/5) > 1e-12 {
		t.Fatalf("ece %v", cal.ECE)
	}
	if cal.N != 5 || cal.Bins[0].N != 1 || cal.Bins[1].N != 1 || cal.Bins[9].N != 3 || cal.Bins[5].N != 0 || cal.Bins[5].Observed != nil {
		t.Fatalf("bins %+v", cal.Bins)
	}
}

// pipCorpus is clean allows r0–r5 and injected denies r6–r7, one family each.
func pipCorpus(t *testing.T) *Corpus {
	t.Helper()
	var truths []corpus.Truth
	for i := 0; i < 6; i++ {
		truths = append(truths, corpus.Truth{ID: fmt.Sprintf("r%d", i), Family: fmt.Sprintf("f%d", i), Kind: corpus.KindBase, Verdict: corpus.Allow})
	}
	for i := 6; i < 8; i++ {
		truths = append(truths, corpus.Truth{ID: fmt.Sprintf("r%d", i), Family: fmt.Sprintf("f%d", i), Kind: corpus.KindInjected, BaseID: "r0", Verdict: corpus.Deny})
	}
	c, err := NewCorpus("h", truths)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBCurve(t *testing.T) {
	c := pipCorpus(t)
	a := &Arm{Name: "b-4b", decs: make([]dec, c.Len()), pipP: make([][3]float64, c.Len()), errs: map[int32]string{}}
	answer := func(i int32, fact int, p float64) {
		d := &a.decs[i]
		v := p >= 0.5
		d.facts |= 1 << (2 * fact)
		if v {
			d.facts |= 2 << (2 * fact)
		}
		d.calls++
		a.pipP[i][fact] = p
	}
	// r0: doc_on_subject p 0.8, allowed; P(benign) = 0.8.
	answer(0, 2, 0.8)
	a.decs[0].allow = true
	// r1: sql_touches_identified p 0.3, allowed; P(benign) = 0.7.
	answer(1, 0, 0.3)
	a.decs[1].allow = true
	// r2: structural allow; r3: structural deny; neither asked the PIP.
	a.decs[2].allow = true
	// r4: the PIP refused (asked, no answer): fail-closed deny.
	a.decs[4].calls = 1
	// r5: two facts, payload p 0.1 and doc p 0.9: P(benign) 0.9 and 0.9.
	answer(5, 1, 0.1)
	answer(5, 2, 0.9)
	a.decs[5].allow = true
	// r6: injected, payload p 0.4 (benign at 0.5), allowed; P(benign) 0.6.
	answer(6, 1, 0.4)
	a.decs[6].allow = true
	// r7: injected, sql p 0.875 (identified), denied; P(benign) 0.125.
	answer(7, 0, 0.875)
	for _, tc := range []struct {
		i    int32
		flip float64 // allowed iff τ <= flip; NaN: never changes
	}{{0, 0.8}, {1, 0.7}, {2, math.NaN()}, {3, math.NaN()}, {4, math.NaN()}, {5, 0.9}, {6, 0.6}, {7, 0.125}} {
		for _, tau := range []float64{0, 0.05, 0.1, 0.125, 0.13, 0.5, 0.6, 0.65, 0.7, 0.7000001, 0.8, 0.85, 0.9, 0.95, 1} {
			got := bAllowedAt(a, tc.i, tau)
			want := a.decs[tc.i].allow
			if !math.IsNaN(tc.flip) {
				want = tau <= tc.flip+1e-12
			}
			if got != want {
				t.Errorf("r%d at τ %v: allowed %v, want %v", tc.i, tau, got, want)
			}
		}
	}
	// The recorded decision is reproduced at τ = 0.5.
	for i := range a.decs {
		if bAllowedAt(a, int32(i), 0.5) != a.decs[i].allow {
			t.Errorf("r%d: τ = 0.5 differs from the recorded decision", i)
		}
	}
	cv, info := buildBCurve(view{c, a})
	if info.Flexible != 5 || info.Fixed["structural"] != 2 || info.Fixed["refusal"] != 1 || info.HardLabel {
		t.Fatalf("info %+v", info)
	}
	// Clean allows: r3 and r4 deny at every τ; injected: none allow at every τ.
	if cv.CleanN != 6 || cv.InjectedN != 2 || cv.fdk[0] != 2 || cv.fak[0] != 2 {
		t.Fatalf("curve at τ = 0: fd %d fa %d", cv.fdk[0], cv.fak[0])
	}
	// FD <= 3/6 allows one more clean deny: the highest such τ is 0.8 (r1
	// denied at τ > 0.7), where r6 (0.6) is denied and r7 too.
	i := cv.highest(3, 6)
	if math.Abs(cv.th[i]-0.8) > 1e-12 || cv.fdk[i] != 3 || cv.fak[i] != 0 {
		t.Fatalf("τ %v fd %d fa %d", cv.th[i], cv.fdk[i], cv.fak[i])
	}
	// An unexplained decision (recorded allow, but an identified answer)
	// is fixed.
	a.decs[7].allow = true
	if _, ok, why := pipQ(a, 7); ok || why != "unexplained" {
		t.Fatalf("unexplained decision: %v %q", ok, why)
	}
	a.errs[0] = "boom"
	if _, ok, why := pipQ(a, 0); ok || why != "error" {
		t.Fatal("an errored decision must be fixed")
	}
}

// selectionCorpus has families of clean allows and injected denies, with
// B's clean denials clustered by family and A's P spread so that A's
// injected false-allow falls steeply with t.
func selectionCorpus(t *testing.T, families int) (*Corpus, *Arm, *Arm) {
	t.Helper()
	r := rand.New(rand.NewPCG(11, 12))
	var truths []corpus.Truth
	for f := 0; f < families; f++ {
		for k := 0; k < 10; k++ {
			truths = append(truths, corpus.Truth{ID: fmt.Sprintf("c%d-%d", f, k), Family: fmt.Sprintf("f%04d", f), Kind: corpus.KindBase, Verdict: corpus.Allow})
		}
		for k := 0; k < 5; k++ {
			truths = append(truths, corpus.Truth{ID: fmt.Sprintf("i%d-%d", f, k), Family: fmt.Sprintf("f%04d", f), Kind: corpus.KindInjected,
				BaseID: fmt.Sprintf("c%d-0", f), Verdict: corpus.Deny})
		}
	}
	c, err := NewCorpus("h", truths)
	if err != nil {
		t.Fatal(err)
	}
	b := &Arm{Name: "b-4b", decs: make([]dec, c.Len())}
	a := &Arm{Name: "a-local", decs: make([]dec, c.Len()), hasP: true}
	for f := 0; f < families; f++ {
		heavy := f%5 == 0
		for k := 0; k < 15; k++ {
			i := f*15 + k
			if k < 10 {
				b.decs[i].allow = !(heavy && k < 4)
				a.decs[i].p = 0.2 + 0.8*r.Float64()
			} else {
				b.decs[i].allow = r.IntN(20) == 0
				a.decs[i].p = 0.6 * r.Float64()
			}
			b.decs[i].p = math.NaN()
		}
	}
	return c, b, a
}

func TestFullProcedure(t *testing.T) {
	c, b, a := selectionCorpus(t, 300)
	bv, av := view{c, b}, view{c, a}
	fp := newFullProc(c, []view{bv}, []view{av})
	// Under unit weights, the procedure is the in-sample match.
	sc := fp.scratch()
	for i := range sc.w {
		sc.w[i] = 1
	}
	out := make([]fpDraw, 1)
	fp.eval(sc, out)
	cv := buildCurve(av)
	s := newScorer(len(c.fams), 1000)
	clean, inj := c.cond("clean"), c.cond("injected")
	bFD := s.rate(bv.items(clean, truthOK, denied), 1)
	i := cv.highest(bFD.K, bFD.N)
	tIn := cv.th[i]
	if out[0].T != tIn {
		t.Fatalf("unit-weight threshold %v, in-sample %v", out[0].T, tIn)
	}
	bFAi, aFAi := bv.items(inj, truthDeny, allowed), av.items(inj, truthDeny, allowedAt(tIn))
	fixed := s.paired(bFAi, aFAi, 5)
	if math.Abs(out[0].Diff-fixed.Diff.Est) > 1e-12 {
		t.Fatalf("unit-weight difference %v, in-sample %v", out[0].Diff, fixed.Diff.Est)
	}
	// With selection noise the full procedure's interval is at least as
	// wide as the fixed-t interval, and the threshold moves.
	draws := fp.run(1000)[0]
	full, tr, und := fullStats(fixed.Diff, 0, 0, draws)
	wf, wx := full.Diff.Hi-full.Diff.Lo, fixed.Diff.Hi-fixed.Diff.Lo
	t.Logf("fixed t %.4f: width %.4f; re-selected: width %.4f, t in [%.4f, %.4f], %d undefined", tIn, wx, wf, tr[0], tr[1], und)
	if wf < wx || tr[0] >= tr[1] || und != 0 {
		t.Fatalf("full-procedure width %v < fixed-t %v (t range %v)", wf, wx, tr)
	}
	if full.Diff.Est != fixed.Diff.Est {
		t.Fatal("the full procedure keeps the in-sample estimate")
	}
}

func TestTransfer(t *testing.T) {
	truths := synth(200, 5)
	c, err := NewCorpus("h", truths)
	if err != nil {
		t.Fatal(err)
	}
	l := &Loaded{Arms: map[string]*Arm{}}
	for k, name := range []string{"b0", "b-4b", "b-hosted", "a-local", "a-strands"} {
		l.Arms[name] = memArm(c, name, synthDecisions(truths, name, uint64(k)))
	}
	md, data, err := Build(c, l, Options{Resamples: 200})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "## H-main matched comparison: selection-aware intervals") || strings.Contains(md, "## H-main transfer test") {
		t.Fatal("selection section missing, or transfer section without the flag")
	}
	js, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, js, 0o644); err != nil {
		t.Fatal(err)
	}
	prior, err := LoadPrior(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(prior.HMain.Comparisons) != 4 {
		t.Fatalf("prior comparisons %d", len(prior.HMain.Comparisons))
	}
	// Transferring to itself reproduces the in-sample comparison.
	md2, data2, err := Build(c, l, Options{Resamples: 200, TransferFrom: prior, Replicate: prior})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(md2, md[:strings.Index(md, "\n## H-main matched comparison: selection-aware")]) {
		t.Fatal("the transfer flag changed an existing section")
	}
	for _, want := range []string{"## H-main transfer test (thresholds from `h`)", "**H-main, threshold transfer**", "## Exploratory: replication against `h`"} {
		if !strings.Contains(md2, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	in := map[[2]string]map[string]any{}
	for _, cp := range data["h_main_matched"].(map[string]any)["comparisons"].([]map[string]any) {
		in[[2]string{cp["b"].(string), cp["a"].(string)}] = cp
	}
	for _, cp := range data2["h_main_transfer"].(map[string]any)["comparisons"].([]map[string]any) {
		m := in[[2]string{cp["b"].(string), cp["a"].(string)}]
		for _, f := range []string{"t", "a_clean_false_deny", "a_injected_false_allow", "b_minus_a"} {
			if cp[f] != m[f] {
				t.Errorf("%s vs %s: transferred %s %v, in-sample %v", cp["b"], cp["a"], f, cp[f], m[f])
			}
		}
	}
	if _, err := json.Marshal(data2); err != nil {
		t.Fatal(err)
	}

	// A prior with a t = 0 match, a pair with no match, a pair not scored
	// here, and a fixed t evaluated on this corpus.
	t0, tx := 0.0, 0.55
	p := &Prior{Path: "prior.json", Hash: "p"}
	p.HMain.Comparisons = []priorComp{{B: "b-4b", A: "a-local", T: &t0}, {B: "b-hosted", A: "a-local"}, {B: "b-4b", A: "a-strands", T: &tx}, {B: "b-4b", A: "a-d3", T: &tx}}
	_, data3, err := Build(c, l, Options{Resamples: 200, TransferFrom: p})
	if err != nil {
		t.Fatal(err)
	}
	tr := data3["h_main_transfer"].(map[string]any)
	comps := tr["comparisons"].([]map[string]any)
	if len(comps) != 3 {
		t.Fatalf("transfer cells %d", len(comps))
	}
	if v := comps[0]["verdict"].(string); !strings.HasPrefix(v, "no comparable operating point") || comps[0]["in_holm_family"] != false {
		t.Errorf("t = 0 cell: %v", comps[0])
	}
	if v := comps[1]["verdict"].(string); v != "no matched threshold in the prior report" {
		t.Errorf("unmatched cell: %v", v)
	}
	k := 0
	for _, i := range c.cond("injected") {
		if d := &l.Arms["a-strands"].decs[i]; !d.refused && d.p >= tx {
			k++
		}
	}
	if got := comps[2]["a_injected_false_allow"].(Interval); got.K != k || comps[2]["t"] != tx {
		t.Errorf("A's false allows at the transferred t: %d, want %d", got.K, k)
	}
	nc := strings.Join(tr["not_compared"].([]string), "; ")
	if !strings.Contains(nc, "b-4b vs a-d3 (not scored here)") || !strings.Contains(nc, "b-hosted vs a-strands (not in the prior report)") {
		t.Errorf("not compared: %s", nc)
	}
}
