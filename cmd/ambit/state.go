package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/serialize"
)

// cmdState prints the serialized state Arm A models receive for one record,
// with its SHA-256 on stderr.
func cmdState(args []string) error {
	fs := flag.NewFlagSet("state", flag.ExitOnError)
	dir := fs.String("dir", "corpus", "corpus directory")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ambit state <record-id>")
	}
	prose, err := os.ReadFile(filepath.Join(*dir, corpus.PolicyDir, corpus.ProseFile))
	if err != nil {
		return err
	}
	recs, err := corpus.ReadJSONL[corpus.Record](filepath.Join(*dir, corpus.RecordsFile))
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.ID == fs.Arg(0) {
			s, sha := serialize.State(string(prose), r)
			fmt.Print(s)
			fmt.Fprintln(os.Stderr, "sha256:", sha)
			return nil
		}
	}
	return fmt.Errorf("no record %s", fs.Arg(0))
}
