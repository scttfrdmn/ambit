package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scttfrdmn/ambit/internal/report"
)

// cmdSwarmReport scores the latest complete swarm run of each arm at each
// agent count and writes swarm-report.md and swarm-report.json.
func cmdSwarmReport(args []string) error {
	fs := flag.NewFlagSet("swarm-report", flag.ExitOnError)
	results := fs.String("results", "results", "results directory")
	out := fs.String("out", "", "where to write swarm-report.md and swarm-report.json (default: the results directory)")
	resamples := fs.Int("resamples", report.DefaultResamples, "bootstrap resamples per interval")
	fs.Parse(args)
	if *out == "" {
		*out = *results
	}
	runs, err := report.LoadSwarm(*results)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return fmt.Errorf("no complete swarm runs under %s", *results)
	}
	md, data := report.BuildSwarm(runs, report.Options{Resamples: *resamples})
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "swarm-report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "swarm-report.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Print(md)
	return nil
}
