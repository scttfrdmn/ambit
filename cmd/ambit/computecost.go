package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/run"
)

// onDemandPerHour is the us-east-1 on-demand list price, in dollars per hour,
// of the smallest instance carrying each GPU the runs used (truffle, live,
// 2026-10-24). An L4 run may have been on a g6.2xlarge ($0.9776); pricing it
// at the g6.xlarge understates the cost of the arms that call a model on
// every decision, which is the conservative direction for the comparison.
var onDemandPerHour = map[string]struct {
	Type  string
	Price float64
}{
	"NVIDIA L40S": {"g6e.xlarge", 1.861},
	"NVIDIA A10G": {"g5.xlarge", 1.006},
	"NVIDIA L4":   {"g6.xlarge", 0.8048},
	"Tesla T4":    {"g4dn.xlarge", 0.526},
}

// cmdComputeCost prices the self-hosted arms' GPU time (#33): shard wall time
// on the instance that ran it, at on-demand list price, per 10k decisions.
// A second column apportions each instance's whole job time (model download
// and server start included) over its shards by wall time. Hosted arms are
// priced by their API spend in the report and are not listed here.
func cmdComputeCost(args []string) error {
	fs := flag.NewFlagSet("compute-cost", flag.ExitOnError)
	dir := fs.String("dir", "corpus", "corpus directory")
	results := fs.String("results", "results", "results directory (with spore-logs/)")
	fs.Parse(args)
	hash, err := corpus.Verify(*dir)
	if err != nil {
		return err
	}
	type shard struct {
		arm, host string
		started   time.Time
		hours     float64
		records   int
	}
	// The report scores the latest complete cover; a range two complete
	// runs cover is priced, as scored, by the later one, and the earlier
	// run's GPU time is counted as duplicated work.
	byKey := map[string]shard{}
	dupRuns, dupHours := 0, 0.0
	ms, _ := filepath.Glob(filepath.Join(*results, "*", "manifest.json"))
	for _, p := range ms {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var m run.Manifest
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if m.CorpusHash != hash || m.Finished == "" || m.Decided != m.Records || m.Stopped != "" || len(m.RepairOf) > 0 {
			continue
		}
		arm := m.Arm
		if m.Ablation != "" && !strings.HasSuffix(arm, "-ablation") {
			arm += "-ablation"
		}
		s, err1 := time.Parse(time.RFC3339, m.Started)
		f, err2 := time.Parse(time.RFC3339, m.Finished)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("%s: unreadable start or finish", p)
		}
		sh := shard{arm, m.Host, s, f.Sub(s).Hours(), m.Records}
		key := fmt.Sprintf("%s %d %d", arm, m.RangeStart, m.RangeEnd)
		if old, ok := byKey[key]; ok {
			dupRuns++
			if sh.started.Before(old.started) {
				sh, old = old, sh
			}
			dupHours += old.hours
		}
		byKey[key] = sh
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	shards := make([]shard, 0, len(keys))
	for _, k := range keys {
		shards = append(shards, byKey[k])
	}

	// Each instance's GPU and whole job time, from its job log.
	insts := map[string]*jobLog{}
	for _, sh := range shards {
		if _, ok := insts[sh.host]; ok {
			continue
		}
		logs, _ := filepath.Glob(filepath.Join(*results, "spore-logs", "*", sh.host+".job.log"))
		if len(logs) == 0 {
			insts[sh.host] = nil // not a spore.host instance: a hosted arm
			continue
		}
		in, err := readJobLog(logs[0])
		if err != nil {
			return err
		}
		insts[sh.host] = in
	}
	shardHours := map[string]float64{}
	for _, sh := range shards {
		shardHours[sh.host] += sh.hours
	}

	type row struct {
		decisions          int
		hours, cost, whole float64
		gpus               map[string]int
	}
	rows := map[string]*row{}
	for _, sh := range shards {
		in := insts[sh.host]
		if in == nil {
			continue
		}
		price, ok := onDemandPerHour[in.gpu]
		if !ok {
			return fmt.Errorf("%s: no price for GPU %q", sh.host, in.gpu)
		}
		r := rows[sh.arm]
		if r == nil {
			r = &row{gpus: map[string]int{}}
			rows[sh.arm] = r
		}
		r.decisions += sh.records
		r.hours += sh.hours
		r.cost += sh.hours * price.Price
		whole := sh.hours
		if shardHours[sh.host] > 0 && in.hours > shardHours[sh.host] {
			whole = in.hours * sh.hours / shardHours[sh.host]
		}
		r.whole += whole * price.Price
		r.gpus[in.gpu]++
	}
	arms := make([]string, 0, len(rows))
	for a := range rows {
		arms = append(arms, a)
	}
	sort.Strings(arms)
	fmt.Printf("Self-hosted GPU cost, corpus `%.12s`. Shard wall time on the instance that ran it, at the us-east-1 on-demand list price of the smallest instance with that GPU ", hash)
	fmt.Printf("(L40S g6e.xlarge $1.861/h, A10G g5.xlarge $1.006/h, L4 g6.xlarge $0.8048/h, T4 g4dn.xlarge $0.526/h). ")
	fmt.Printf("\"with setup\" spreads each instance's whole job time (weights download, server start) over its shards. Spot runs cost less; instance boot before the job is not counted.\n\n")
	fmt.Println("| arm | decisions | GPU (shards) | GPU-hours | $ per 10k decisions | $ per 10k, with setup |")
	fmt.Println("|---|---|---|---|---|---|")
	for _, a := range arms {
		r := rows[a]
		var gs []string
		for g, n := range r.gpus {
			gs = append(gs, fmt.Sprintf("%s (%d)", strings.TrimPrefix(strings.TrimPrefix(g, "NVIDIA "), "Tesla "), n))
		}
		sort.Strings(gs)
		k := float64(r.decisions) / 10000
		fmt.Printf("| %s | %d | %s | %.1f | $%.2f | $%.2f |\n", a, r.decisions, strings.Join(gs, ", "), r.hours, r.cost/k, r.whole/k)
	}
	if dupRuns > 0 {
		fmt.Printf("\nDuplicated runs: %d, %.1f run-hours (fleet relaunches of a range another complete run covers); the later run is the one scored and priced.\n", dupRuns, dupHours)
	}
	return nil
}

// jobLog is what an instance's job log says: its GPU, and how long the job
// ran.
type jobLog struct {
	gpu   string
	hours float64
}

// readJobLog reads an instance's GPU (the log's second line, from
// nvidia-smi) and the span between its first and last timestamps.
func readJobLog(path string) (*jobLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var first, last time.Time
	gpu := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 0; sc.Scan(); n++ {
		line := sc.Text()
		if n == 1 {
			gpu, _, _ = strings.Cut(line, ",")
		}
		if ts, _, ok := strings.Cut(line, " "); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				if first.IsZero() {
					first = t
				}
				last = t
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return &jobLog{gpu, last.Sub(first).Hours()}, nil
}
