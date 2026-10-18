package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/scttfrdmn/ambit/internal/report"
)

// cmdReport scores each arm's complete runs on one corpus and writes
// report.md and report.json.
func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dir := fs.String("dir", "corpus", "corpus directory")
	results := fs.String("results", "results", "results directory")
	out := fs.String("out", "", "where to write report.md and report.json (default: the results directory)")
	resamples := fs.Int("resamples", report.DefaultResamples, "bootstrap resamples per interval")
	fs.Parse(args)
	if *out == "" {
		*out = *results
	}
	start := time.Now()
	c, err := report.LoadCorpus(*dir)
	if err != nil {
		return err
	}
	loaded, err := report.Load(*results, c)
	if err != nil {
		return err
	}
	if len(loaded.Arms) == 0 {
		return fmt.Errorf("no complete runs on corpus %.12s under %s", c.Hash, *results)
	}
	fmt.Fprintf(os.Stderr, "loaded %d records and %d arms in %s\n", c.Len(), len(loaded.Arms), time.Since(start).Round(time.Millisecond))
	md, data, err := report.Build(c, loaded, report.Options{Resamples: *resamples})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "report.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "report in %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Print(md)
	return nil
}
