package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/cedarq"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/hosted"
	"github.com/scttfrdmn/ambit/internal/jeb"
	"github.com/scttfrdmn/ambit/internal/run"
	"github.com/scttfrdmn/ambit/internal/serialize"
	"github.com/scttfrdmn/ambit/internal/strands"
)

// runCapMicros is the per-run hosted cap (Law 6). The project or gate
// ceiling is the -ceiling flag, checked against the spend ledger in the
// results directory: keep one results directory per gate.
const runCapMicros = 5_000_000

// ablations are the registered Arm A prose ablations.
var ablations = map[string]func(string) (string, error){
	"failclosed": serialize.WithoutFailClosed,
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	arm := fs.String("arm", "b0", "b0 | b | b-4b | b-hosted | a-local | a-hosted")
	dir := fs.String("dir", "corpus", "corpus directory")
	results := fs.String("results", "results", "results directory")
	offset := fs.Int("offset", 0, "skip the first n records (a shard's start)")
	limit := fs.Int("limit", 0, "decide only n records from the offset (smoke test, or a shard's size)")
	concurrency := fs.Int("concurrency", 1, "records decided at once")
	cachePrompt := fs.Bool("cache-prompt", false, "let llama-server reuse cached prompt prefixes")
	ablation := fs.String("ablation", "", "Arm A prose ablation: failclosed")
	name := fs.String("name", "", "arm and run name (default: the arm, plus -ablation if any)")
	llama := fs.String("llama", "http://127.0.0.1:8080", "llama-server serving the pinned Jebadiah GGUF")
	ceiling := fs.Float64("ceiling", 200, "hosted spend ceiling in dollars for this results directory's ledger (G1 $200, G2 $300, G3 $500)")
	repair := fs.String("repair", "", "comma-separated run directories: re-ask only the records whose decisions there are errors")
	strandsConcurrent := fs.Bool("strands-concurrent", false, "send strands-decider requests concurrently (Linux CUDA only; see internal/strands)")
	strandsURL := fs.String("strands", "http://127.0.0.1:8000", "strands-decider serve, with the pinned hobson-v21 checkpoint")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	pol, err := cedarq.LoadPolicies(*dir)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(*dir, corpus.PolicyDir, corpus.ProseFile))
	if err != nil {
		return err
	}
	prose := string(b)
	isA := *arm == "a-local" || *arm == "a-hosted" || *arm == "a-strands"
	if *ablation != "" {
		ablate, ok := ablations[*ablation]
		if !ok {
			return fmt.Errorf("unknown ablation %q", *ablation)
		}
		if !isA {
			return fmt.Errorf("ablation %s applies to Arm A only", *ablation)
		}
		if prose, err = ablate(prose); err != nil {
			return err
		}
	}
	armName := *arm
	if *ablation != "" {
		armName += "-ablation"
	}
	if *name != "" {
		armName = *name
	}
	opt := run.Options{CorpusDir: *dir, ResultsDir: *results, Offset: *offset, Limit: *limit,
		Concurrency: *concurrency, Models: map[string]string{}, Ablation: *ablation}
	if isA {
		h := sha256.Sum256([]byte(prose))
		opt.ProseSHA256 = hex.EncodeToString(h[:])
	}
	if *repair != "" {
		if opt.IDs, opt.RepairOf, err = erroredRecords(strings.Split(*repair, ","), armName); err != nil {
			return err
		}
		if len(opt.IDs) == 0 {
			fmt.Println("no errored decisions to repair")
			return nil
		}
	}

	local := func() (*arms.Jeb, error) {
		c, err := jeb.New(ctx, *llama)
		if err != nil {
			return nil, fmt.Errorf("llama-server at %s: %w (see README: Running the local model)", *llama, err)
		}
		c.CachePrompt = *cachePrompt
		opt.CachePrompt = *cachePrompt
		j := &arms.Jeb{C: c, Pin: jeb.Jebadiah4BV2}
		opt.Models["jebadiah"] = j.ID() + " sha256:" + jeb.Jebadiah4BV2.FileSHA256
		return j, nil
	}
	decider := func() (*strands.Client, error) {
		c, err := strands.New(ctx, *strandsURL)
		if err != nil {
			return nil, fmt.Errorf("strands-decider at %s: %w", *strandsURL, err)
		}
		if *strandsConcurrent && c.Health.Device != "cuda" {
			return nil, fmt.Errorf("-strands-concurrent needs a CUDA server; this one is on %q", c.Health.Device)
		}
		c.Concurrent = *strandsConcurrent
		opt.Models["strands"] = strands.HobsonV21.ID() + " base " + strands.HobsonV21.BaseRepo + "@" + strands.HobsonV21.BaseRevision +
			" server " + strands.HobsonV21.Server
		return c, nil
	}
	remote := func() (*arms.Hosted, error) {
		l, err := hosted.OpenLedger(filepath.Join(*results, "spend.jsonl"), armName, runCapMicros, int64(*ceiling*1e6))
		if err != nil {
			return nil, err
		}
		c, err := hosted.FromEnv(l)
		if err != nil {
			return nil, err
		}
		h := &arms.Hosted{C: c}
		opt.Models["hosted"] = h.ID()
		opt.Spent = l.RunSpent
		return h, nil
	}

	var g arms.Gate
	switch *arm {
	case "b0":
		g = &arms.Cedar{Arm: armName, Policies: pol}
	case "b", "b-4b":
		// b is G1's name for Cedar with the local Jebadiah PIP; b-4b names
		// the same gate beside other PIP sizes.
		j, err := local()
		if err != nil {
			return err
		}
		g = &arms.Cedar{Arm: armName, Policies: pol, PIP: j}
	case "b-hosted":
		h, err := remote()
		if err != nil {
			return err
		}
		g = &arms.Cedar{Arm: armName, Policies: pol, PIP: h}
	case "a-local":
		j, err := local()
		if err != nil {
			return err
		}
		g = &arms.Model{Arm: armName, Policy: prose, M: j}
	case "a-hosted":
		h, err := remote()
		if err != nil {
			return err
		}
		g = &arms.Model{Arm: armName, Policy: prose, M: h}
	case "a-strands":
		c, err := decider()
		if err != nil {
			return err
		}
		g = &arms.Strands{Arm: armName, Policy: prose, C: c, Pin: strands.HobsonV21}
	case "b-strands":
		c, err := decider()
		if err != nil {
			return err
		}
		g = &arms.Cedar{Arm: armName, Policies: pol, PIP: &arms.StrandsPIP{C: c, Pin: strands.HobsonV21}}
	default:
		return fmt.Errorf("unknown arm %q", *arm)
	}

	start := time.Now()
	opt.Progress = func(i, n int, d arms.Decision) {
		if i%max(50, n/200) == 0 || i == n {
			fmt.Fprintf(os.Stderr, "%s: %d/%d (%s)\n", armName, i, n, time.Since(start).Round(time.Second))
		}
		if d.Error != "" {
			fmt.Fprintf(os.Stderr, "%s: %s: error: %s\n", armName, d.ID, d.Error)
		}
	}
	m, err := run.Run(ctx, g, opt)
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
			if dec.Error != "" {
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
