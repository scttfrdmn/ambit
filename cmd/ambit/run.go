package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/cedarq"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/hosted"
	"github.com/scttfrdmn/ambit/internal/jeb"
	"github.com/scttfrdmn/ambit/internal/run"
)

// Spend caps (Law 6), in micro-dollars.
const (
	runCapMicros     = 5_000_000
	projectCapMicros = 200_000_000
)

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	arm := fs.String("arm", "b0", "b0 | b | b-hosted | a-local | a-hosted")
	dir := fs.String("dir", "corpus", "corpus directory")
	results := fs.String("results", "results", "results directory")
	limit := fs.Int("limit", 0, "decide only the first n records (smoke test)")
	llama := fs.String("llama", "http://127.0.0.1:8080", "llama-server serving the pinned Jebadiah GGUF")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	pol, err := cedarq.LoadPolicies(*dir)
	if err != nil {
		return err
	}
	prose, err := os.ReadFile(filepath.Join(*dir, corpus.PolicyDir, corpus.ProseFile))
	if err != nil {
		return err
	}
	opt := run.Options{CorpusDir: *dir, ResultsDir: *results, Limit: *limit, Models: map[string]string{}}

	local := func() (*arms.Jeb, error) {
		c, err := jeb.New(ctx, *llama)
		if err != nil {
			return nil, fmt.Errorf("llama-server at %s: %w (see README: Running the local model)", *llama, err)
		}
		j := &arms.Jeb{C: c, Pin: jeb.Jebadiah4BV2}
		opt.Models["jebadiah"] = j.ID() + " sha256:" + jeb.Jebadiah4BV2.FileSHA256
		return j, nil
	}
	remote := func(name string) (*arms.Hosted, error) {
		l, err := hosted.OpenLedger(filepath.Join(*results, "spend.jsonl"), name, runCapMicros, projectCapMicros)
		if err != nil {
			return nil, err
		}
		c, err := hosted.FromEnv(l)
		if err != nil {
			return nil, err
		}
		h := &arms.Hosted{C: c}
		opt.Models["hosted"] = h.ID()
		opt.Before = func(r corpus.Record) { h.Record = r.ID }
		opt.Spent = l.RunSpent
		return h, nil
	}

	var g arms.Gate
	switch *arm {
	case "b0":
		g = &arms.Cedar{Arm: "b0", Policies: pol}
	case "b":
		j, err := local()
		if err != nil {
			return err
		}
		g = &arms.Cedar{Arm: "b", Policies: pol, PIP: j}
	case "b-hosted":
		h, err := remote("b-hosted")
		if err != nil {
			return err
		}
		g = &arms.Cedar{Arm: "b-hosted", Policies: pol, PIP: h}
	case "a-local":
		j, err := local()
		if err != nil {
			return err
		}
		g = &arms.Model{Arm: "a-local", Policy: string(prose), M: j}
	case "a-hosted":
		h, err := remote("a-hosted")
		if err != nil {
			return err
		}
		g = &arms.Model{Arm: "a-hosted", Policy: string(prose), M: h}
	default:
		return fmt.Errorf("unknown arm %q", *arm)
	}

	start := time.Now()
	opt.Progress = func(i, n int, d arms.Decision) {
		if i%50 == 0 || i == n {
			fmt.Fprintf(os.Stderr, "%s: %d/%d (%s)\n", *arm, i, n, time.Since(start).Round(time.Second))
		}
		if d.Error != "" {
			fmt.Fprintf(os.Stderr, "%s: %s: error: %s\n", *arm, d.ID, d.Error)
		}
	}
	m, err := run.Run(ctx, g, opt)
	if err != nil {
		return err
	}
	fmt.Printf("run %s: %d/%d decided, %d errors, spend $%.4f, corpus %s\n",
		m.Run, m.Decided, m.Records, m.Errors, float64(m.SpendMicros)/1e6, m.CorpusHash[:12])
	if m.Stopped != "" {
		return fmt.Errorf("run stopped: %s", m.Stopped)
	}
	return nil
}
