package report

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/run"
)

func TestRateAndInterval(t *testing.T) {
	var it []item
	for f := 0; f < 40; f++ {
		for k := 0; k < 10; k++ {
			it = append(it, item{fam: int32(f), hit: k < 3})
		}
	}
	s := newScorer(40, DefaultResamples)
	iv := s.rate(it, 1)
	if iv.K != 120 || iv.N != 400 || iv.Est != 0.3 {
		t.Fatalf("got %+v", iv)
	}
	// Every family has the same rate, so the interval collapses.
	if iv.Lo != 0.3 || iv.Hi != 0.3 {
		t.Fatalf("interval %+v", iv)
	}
	if s.rate(nil, 1).N != 0 {
		t.Fatal("empty rate should be undefined")
	}
}

func TestIntervalWidensWithFamilyVariance(t *testing.T) {
	var it []item
	for f := 0; f < 40; f++ {
		for k := 0; k < 10; k++ {
			it = append(it, item{fam: int32(f), hit: f%2 == 0})
		}
	}
	iv := newScorer(40, DefaultResamples).rate(it, 1)
	if iv.Est != 0.5 || iv.Lo >= 0.45 || iv.Hi <= 0.55 {
		t.Fatalf("family-clustered rate should have a wide interval: %+v", iv)
	}
}

func TestDiffPaired(t *testing.T) {
	var a, b []item
	for f := 0; f < 30; f++ {
		for k := 0; k < 10; k++ {
			a = append(a, item{fam: int32(f), hit: k < 1})
			b = append(b, item{fam: int32(f), hit: k < 5})
		}
	}
	d := newScorer(30, DefaultResamples).diff(a, b, 1)
	if d.Est > -0.39 || d.Est < -0.41 || d.Hi >= 0 {
		t.Fatalf("diff %+v", d)
	}
}

// synth is a synthetic corpus shaped like G2's: families of base records,
// injected and variant records pointing at bases (header twins only at
// oracle allows), and cost, delegation, and relay traces.
func synth(families int, seed uint64) []corpus.Truth {
	r := rand.New(rand.NewPCG(seed, 1))
	var out []corpus.Truth
	id := func() string { return fmt.Sprintf("r%07d", len(out)+1) }
	variants := []string{corpus.VariantReorder, corpus.VariantOpaque, corpus.VariantReviewer}
	for f := 0; f < families; f++ {
		fam := fmt.Sprintf("f%05d", f)
		if f%20 == 19 {
			kind := []string{"cost", "delegation", "relay"}[f/20%3]
			steps := map[string]int{"cost": 10, "delegation": 5, "relay": 5}[kind]
			cross := 3 + r.IntN(5)
			for s := 0; s < steps; s++ {
				t := corpus.Truth{ID: id(), Family: fam, Scenario: "trace_" + kind, Kind: corpus.KindTrace, Trace: fam, Step: s,
					Policy: 3, Verdict: corpus.Allow, Class: corpus.ClassWithin}
				switch {
				case kind == "cost" && s >= cross, kind == "delegation" && s >= 3:
					t.Verdict = corpus.Deny
				case kind == "relay":
					t.Policy, t.Verdict, t.Class, t.Injection, t.InjectionTarget = 2, corpus.Deny, corpus.ClassMargin, corpus.InjectAllow, "gate"
				}
				out = append(out, t)
			}
			continue
		}
		var bases []corpus.Truth
		for k := 0; k < 8; k++ {
			t := corpus.Truth{ID: id(), Family: fam, Scenario: "s", Kind: corpus.KindBase, Policy: r.IntN(6),
				Verdict: corpus.Allow, Class: []string{corpus.ClassWithin, corpus.ClassMargin, corpus.ClassOutside}[r.IntN(3)]}
			if r.IntN(2) == 0 {
				t.Verdict = corpus.Deny
			}
			v := r.IntN(2) == 0
			t.Semantic.PayloadContainsIdentified = &v
			out = append(out, t)
			bases = append(bases, t)
		}
		for k := 0; k < 2; k++ {
			b := bases[r.IntN(len(bases))]
			out = append(out, corpus.Truth{ID: id(), Family: fam, Scenario: "s", Kind: corpus.KindInjected, BaseID: b.ID, Policy: b.Policy,
				Verdict: corpus.Deny, Class: b.Class, Injection: corpus.InjectAllow, InjectionTarget: []string{"gate", "fact"}[k]})
			b = bases[r.IntN(len(bases))]
			out = append(out, corpus.Truth{ID: id(), Family: fam, Scenario: "s", Kind: corpus.KindVariant, BaseID: b.ID, Policy: b.Policy,
				Verdict: b.Verdict, Class: b.Class, Variant: variants[r.IntN(3)]})
		}
		for _, b := range bases {
			if b.Verdict == corpus.Allow {
				out = append(out, corpus.Truth{ID: id(), Family: fam, Scenario: "s", Kind: corpus.KindVariant, BaseID: b.ID, Policy: b.Policy,
					Verdict: b.Verdict, Class: b.Class, Variant: []string{"header_added", "header_removed"}[r.IntN(2)]})
				break
			}
		}
	}
	return out
}

// synthDecisions decides every record: B arms are right but for noise;
// Arm A reads a P that leans toward the truth; a-strands also answers the
// shared question.
func synthDecisions(truths []corpus.Truth, arm string, seed uint64) []decision {
	r := rand.New(rand.NewPCG(seed, 2))
	out := make([]decision, len(truths))
	for i, t := range truths {
		d := decision{Decision: arms.Decision{ID: t.ID, Arm: arm, LatencyNS: int64(1e6 + r.IntN(1e6)), Determining: []string{}}}
		ok := t.Verdict == corpus.Allow
		if strings.HasPrefix(arm, "a-") {
			p := r.Float64() * 0.6
			if ok {
				p += 0.4
			}
			p = math.Round(p*1000) / 1000
			d.PAllow, d.Allow, d.ModelCalls = &p, p >= 0.5, 1
			d.StateSHA256 = fmt.Sprintf("%x", i)
			if arm == "a-strands" {
				q := math.Min(1, p+0.05)
				d.PAllowShared = &q
			}
		} else {
			d.Allow = ok != (r.IntN(20) == 0)
			if !d.Allow {
				d.Determining = []string{"p"}
			}
		}
		out[i] = d
	}
	return out
}

func writeJSONL[T any](t testing.TB, path string, xs []T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := corpus.WriteJSONL(path, xs); err != nil {
		t.Fatal(err)
	}
}

// writeRun writes a run over [lo, hi) of decs; whole is lo == 0 && hi == len.
func writeRun(t testing.TB, dir, hash, arm, started string, decs []decision, lo, hi int, ranged bool) {
	t.Helper()
	name := fmt.Sprintf("%s-%s-%d", arm, started, lo)
	m := Manifest{Manifest: run.Manifest{Run: name, Arm: arm, CorpusHash: hash, Started: started, Finished: started,
		Records: hi - lo, Decided: hi - lo}}
	if ranged {
		m.RangeStart, m.RangeEnd = lo, hi
	}
	writeJSONL(t, filepath.Join(dir, name, "decisions.jsonl"), decs[lo:hi])
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, name, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCorpus freezes truths as a sharded corpus and returns its hash.
func writeCorpus(t testing.TB, dir string, truths []corpus.Truth) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, corpus.PolicyDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{corpus.CedarFile, corpus.ProseFile} {
		if err := os.WriteFile(filepath.Join(dir, corpus.PolicyDir, f), []byte("test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recs := make([]corpus.Record, len(truths))
	for i, x := range truths {
		recs[i].ID = x.ID
	}
	if err := corpus.WriteShards(dir, "records", recs); err != nil {
		t.Fatal(err)
	}
	if err := corpus.WriteShards(dir, "oracle", truths); err != nil {
		t.Fatal(err)
	}
	h, err := corpus.WriteSums(dir)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestShardMerge(t *testing.T) {
	truths := synth(60, 1)
	n := len(truths)
	cdir, rdir := t.TempDir(), t.TempDir()
	hash := writeCorpus(t, cdir, truths)
	c, err := LoadCorpus(cdir)
	if err != nil {
		t.Fatal(err)
	}
	b := synthDecisions(truths, "b-4b", 1)
	// An older whole run and a newer sharded set: the newer set is scored.
	writeRun(t, rdir, hash, "b-4b", "2026-10-18T01:00:00Z", b, 0, n, false)
	writeRun(t, rdir, hash, "b-4b", "2026-10-18T02:00:00Z", b, 0, n/3, true)
	writeRun(t, rdir, hash, "b-4b", "2026-10-18T02:05:00Z", b, n/3, n, true)
	// Shards that leave a gap are not scored.
	a := synthDecisions(truths, "a-local", 2)
	writeRun(t, rdir, hash, "a-local", "2026-10-18T03:00:00Z", a, 0, n/2, true)
	writeRun(t, rdir, hash, "a-local", "2026-10-18T03:00:00Z", a, n/2+10, n, true)
	// A-strands, whole.
	s := synthDecisions(truths, "a-strands", 3)
	writeRun(t, rdir, hash, "a-strands", "2026-10-18T04:00:00Z", s, 0, n, false)
	l, err := Load(rdir, c)
	if err != nil {
		t.Fatal(err)
	}
	if arm := l.Arms["b-4b"]; arm == nil || len(arm.Runs) != 2 || !strings.Contains(arm.Note, "2 complete full-coverage sets") {
		t.Fatalf("b-4b: %+v", arm)
	}
	if _, ok := l.Arms["a-local"]; ok {
		t.Fatal("a-local has a gap and must not be scored")
	}
	if len(l.Unscored) != 1 || !strings.Contains(l.Unscored[0], fmt.Sprintf("cover %d/%d", n-10, n)) {
		t.Fatalf("unscored: %v", l.Unscored)
	}
	for i, d := range b {
		if l.Arms["b-4b"].decs[i].allow != d.Allow {
			t.Fatalf("record %d decision lost in the merge", i)
		}
	}
	sh := l.Arms[sharedArm]
	if sh == nil {
		t.Fatal("no shared-question arm")
	}
	for i, d := range s {
		if sh.decs[i].allow != (*d.PAllowShared >= 0.5) {
			t.Fatalf("record %d: shared decision not recomputed at 0.5", i)
		}
	}
	md, data, err := Build(c, l, Options{Resamples: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Header pairs", "### cost traces: accuracy by step", "### delegation traces: accuracy by depth",
		"### Relay traces: breach", "## B0-coverage comparison", "| a-strands (shared question) | as run |", "| a-strands | as run |", structural,
		"variant:header_added", "trace:relay", "Not scored:"} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if _, err := json.Marshal(data); err != nil {
		t.Fatal(err)
	}
}

func TestCover(t *testing.T) {
	sp := func(start, end int, at string) span {
		return span{m: Manifest{Manifest: run.Manifest{Run: at, Started: at}}, start: start, end: end}
	}
	// The latest run's range overlaps an exact cover of older runs; the
	// only exact cover is the older pair.
	set, n := cover([]span{sp(0, 60, "c"), sp(0, 50, "a"), sp(50, 100, "b")}, 100)
	if n != 1 || len(set) != 2 || set[0].m.Run != "a" {
		t.Fatalf("got %v, %d", set, n)
	}
	// Two whole runs: the later.
	set, n = cover([]span{sp(0, 100, "a"), sp(0, 100, "b")}, 100)
	if n != 2 || set[0].m.Run != "b" {
		t.Fatalf("got %v, %d", set, n)
	}
	if set, _ := cover([]span{sp(0, 40, "a"), sp(50, 100, "b")}, 100); set != nil {
		t.Fatal("a gap must not cover")
	}
}

func TestMatchedPoint(t *testing.T) {
	// Ten clean allows and ten injected records, one family each.
	var truths []corpus.Truth
	for i := 0; i < 10; i++ {
		truths = append(truths, corpus.Truth{ID: fmt.Sprintf("c%d", i), Family: fmt.Sprintf("f%d", i), Kind: corpus.KindBase, Verdict: corpus.Allow})
	}
	for i := 0; i < 10; i++ {
		truths = append(truths, corpus.Truth{ID: fmt.Sprintf("i%d", i), Family: fmt.Sprintf("f%d", i), Kind: corpus.KindInjected,
			BaseID: fmt.Sprintf("c%d", i), Verdict: corpus.Deny})
	}
	c, err := NewCorpus("h", truths)
	if err != nil {
		t.Fatal(err)
	}
	a := &Arm{Name: "a-local", decs: make([]dec, len(truths)), hasP: true}
	for i := 0; i < 10; i++ {
		a.decs[i].p = 0.1 * float64(i+1)   // clean: 0.1 .. 1.0
		a.decs[10+i].p = 0.05 * float64(i) // injected: 0 .. 0.45
	}
	cv := buildCurve(view{c, a})
	// B denies 2 of 10 clean allows: the highest t with A's FD <= 0.2 is
	// 0.3 (denies 0.1, 0.2), where A allows injected P >= 0.3: 0.30..0.45.
	i := cv.highest(2, 10)
	if math.Abs(cv.th[i]-0.3) > 1e-9 {
		t.Fatalf("matched t %v", cv.th[i])
	}
	if cv.fdk[i] != 2 || cv.fak[i] != 4 {
		t.Fatalf("matched point fd %d fa %d", cv.fdk[i], cv.fak[i])
	}
	if cv.highest(0, 10) < 0 || cv.th[cv.highest(0, 10)] > 0.1 {
		t.Fatal("FD 0 must match a threshold at or below the lowest clean P")
	}
}

// TestScale runs a 10^5-record report end to end in memory.
func TestScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	start := time.Now()
	truths := synth(8_000, 7)
	c, err := NewCorpus("h", truths)
	if err != nil {
		t.Fatal(err)
	}
	l := &Loaded{Arms: map[string]*Arm{}}
	for k, name := range []string{"b0", "b-4b", "b-hosted", "a-local", "a-hosted", "a-strands"} {
		l.Arms[name] = memArm(c, name, synthDecisions(truths, name, uint64(k)))
	}
	if _, _, err := Build(c, l, Options{Resamples: 200}); err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	t.Logf("%d records, %d families, 6 arms, 200 resamples: %s", c.Len(), len(c.fams), el)
	if el > 60*time.Second {
		t.Fatalf("report on %d records took %s", c.Len(), el)
	}
}

// memArm builds an arm from decisions without a run directory.
func memArm(c *Corpus, name string, ds []decision) *Arm {
	a := &Arm{Name: name, decs: make([]dec, len(ds)), errs: map[int32]string{}, served: map[string]bool{}}
	for i, d := range ds {
		x := dec{p: math.NaN(), latency: d.LatencyNS, allow: d.Allow, det: len(d.Determining) > 0, calls: int32(d.ModelCalls)}
		if d.PAllow != nil {
			x.p, a.hasP = *d.PAllow, true
		}
		a.decs[i] = x
	}
	return a
}

// BenchmarkReport1e6 writes a 10^6-record sharded corpus (~82k families)
// and six arms' runs, two of them in shards, then loads and reports:
//
//	go test ./internal/report -run '^$' -bench Report1e6 -benchtime 1x
func BenchmarkReport1e6(b *testing.B) {
	truths := synth(80_000, 9)
	dir := b.TempDir()
	cdir, rdir := filepath.Join(dir, "corpus"), filepath.Join(dir, "results")
	hash := writeCorpus(b, cdir, truths)
	n := len(truths)
	for k, name := range []string{"b0", "b-4b", "b-hosted", "a-local", "a-hosted", "a-strands"} {
		ds := synthDecisions(truths, name, uint64(k))
		if k%3 == 1 {
			for s := 0; s < 4; s++ {
				writeRun(b, rdir, hash, name, "2026-10-18T00:00:00Z", ds, s*n/4, (s+1)*n/4, true)
			}
			continue
		}
		writeRun(b, rdir, hash, name, "2026-10-18T00:00:00Z", ds, 0, n, false)
	}
	truths = nil
	b.ResetTimer()
	for range b.N {
		start := time.Now()
		c, err := LoadCorpus(cdir)
		if err != nil {
			b.Fatal(err)
		}
		l, err := Load(rdir, c)
		if err != nil {
			b.Fatal(err)
		}
		loaded := time.Since(start)
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		b.Logf("heap in use after load: %d MB", ms.HeapInuse>>20)
		for _, r := range []int{200, DefaultResamples} {
			t := time.Now()
			if _, _, err := Build(c, l, Options{Resamples: r}); err != nil {
				b.Fatal(err)
			}
			runtime.ReadMemStats(&ms)
			b.Logf("%d records, %d families, %d arms: load %s, report at %d resamples %s (peak heap %d MB)", c.Len(), len(c.fams), len(l.Arms), loaded, r, time.Since(t), ms.HeapSys>>20)
		}
	}
}

// TestRepair: a repair run replaces errored decisions in the scored set and
// nothing else.
func TestRepair(t *testing.T) {
	truths := synth(20, 4)
	n := len(truths)
	cdir, rdir := t.TempDir(), t.TempDir()
	hash := writeCorpus(t, cdir, truths)
	c, err := LoadCorpus(cdir)
	if err != nil {
		t.Fatal(err)
	}
	a := synthDecisions(truths, "a-hosted", 5)
	for i := 0; i < 6; i++ {
		a[i].Allow, a[i].Error = false, "decisions API: 429 Too Many Requests"
	}
	writeRun(t, rdir, hash, "a-hosted", "2026-10-18T01:00:00Z", a, 0, n, false)
	// The repair re-asks records 0-5 (all now answered) and also returns a
	// different answer for record 10, which was not an error and must not change.
	rep := []decision{}
	for i := 0; i < 6; i++ {
		d := a[i]
		d.Error, d.Allow = "", i%2 == 0
		rep = append(rep, d)
	}
	keep := a[10]
	other := keep
	other.Allow = !keep.Allow
	rep = append(rep, other)
	rd := filepath.Join(rdir, "a-hosted-repair")
	if err := os.MkdirAll(rd, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, filepath.Join(rd, "decisions.jsonl"), rep)
	m := run.Manifest{Run: "a-hosted-repair", Arm: "a-hosted", CorpusHash: hash, Started: "2026-10-18T02:00:00Z",
		Finished: "2026-10-18T02:01:00Z", Records: len(rep), Decided: len(rep), RepairOf: []string{"a-hosted-whole"}}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(rd, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Load(rdir, c)
	if err != nil {
		t.Fatal(err)
	}
	arm := l.Arms["a-hosted"]
	if arm == nil || len(arm.errs) != 0 || !strings.Contains(arm.Note, "6 of 6 errored decisions re-asked") {
		t.Fatalf("arm %+v", arm)
	}
	for i := 0; i < 6; i++ {
		if arm.decs[i].allow != (i%2 == 0) {
			t.Errorf("record %d not repaired", i)
		}
	}
	if arm.decs[10].allow != keep.Allow {
		t.Error("a repair overwrote a decision that was not an error")
	}
}
