package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/run"
)

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	gf := addGateFlags(fs)
	dir := fs.String("dir", "corpus", "corpus directory")
	results := fs.String("results", "results", "results directory")
	offset := fs.Int("offset", 0, "skip the first n records (a shard's start)")
	limit := fs.Int("limit", 0, "decide only n records from the offset (smoke test, or a shard's size)")
	concurrency := fs.Int("concurrency", 1, "records decided at once")
	repair := fs.String("repair", "", "comma-separated run directories: re-ask only the records whose decisions there are errors")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	armName := gf.armName()
	opt := run.Options{CorpusDir: *dir, ResultsDir: *results, Offset: *offset, Limit: *limit,
		Concurrency: *concurrency, Ablation: *gf.ablation}
	var err error
	if *repair != "" {
		if opt.IDs, opt.RepairOf, err = erroredRecords(strings.Split(*repair, ","), armName); err != nil {
			return err
		}
		if len(opt.IDs) == 0 {
			fmt.Println("no errored decisions to repair")
			return nil
		}
	}

	b, err := gf.build(ctx, *dir, *results)
	if err != nil {
		return err
	}
	opt.Models, opt.CachePrompt, opt.ProseSHA256, opt.Spent = b.models, b.cachePrompt, b.proseSHA256, b.spent
	opt.RunCapMicros = b.runCapMicros

	start := time.Now()
	opt.Progress = func(i, n int, d arms.Decision) {
		if i%max(50, n/200) == 0 || i == n {
			fmt.Fprintf(os.Stderr, "%s: %d/%d (%s)\n", armName, i, n, time.Since(start).Round(time.Second))
		}
		if d.Error != "" {
			fmt.Fprintf(os.Stderr, "%s: %s: error: %s\n", armName, d.ID, d.Error)
		}
	}
	m, err := run.Run(ctx, b.gate, opt)
	if err != nil {
		return err
	}
	fmt.Printf("run %s: %d/%d decided (records %d-%d), %d errors, spend $%.4f, corpus %s\n",
		m.Run, m.Decided, m.Records, m.RangeStart, m.RangeEnd, m.Errors, float64(m.SpendMicros)/1e6, m.CorpusHash[:12])
	if m.Stopped != "" {
		return fmt.Errorf("run stopped: %s", m.Stopped)
	}
	return nil
}

// erroredRecords collects the records whose decisions are errors in the given
// run directories, which must all be runs of arm.
func erroredRecords(dirs []string, arm string) (map[string]bool, []string, error) {
	ids := map[string]bool{}
	var runs []string
	for _, d := range dirs {
		var m run.Manifest
		b, err := os.ReadFile(filepath.Join(d, "manifest.json"))
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", d, err)
		}
		if m.Arm != arm {
			return nil, nil, fmt.Errorf("%s is a run of %s, not %s", d, m.Arm, arm)
		}
		runs = append(runs, m.Run)
		err = corpus.Each(d, "decisions", func(dec arms.Decision) error {
			if run.Repairable(dec.Error) {
				ids[dec.ID] = true
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return ids, runs, nil
}
