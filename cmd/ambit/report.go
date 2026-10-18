package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/report"
)

// cmdReport scores the latest complete run of each arm and writes
// results/report.md and results/report.json.
func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dir := fs.String("dir", "corpus", "corpus directory")
	results := fs.String("results", "results", "results directory")
	fs.Parse(args)
	hash, err := corpus.Verify(*dir)
	if err != nil {
		return err
	}
	truth, err := corpus.ReadJSONL[corpus.Truth](filepath.Join(*dir, corpus.OracleFile))
	if err != nil {
		return err
	}
	runs, err := report.Load(*results, hash)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return fmt.Errorf("no complete runs under %s", *results)
	}
	md, data, err := report.Build(hash, truth, runs)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*results, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	if err := os.WriteFile(filepath.Join(*results, "report.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Print(md)
	return nil
}
