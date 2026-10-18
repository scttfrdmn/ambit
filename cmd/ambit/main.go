// Command ambit generates the corpus, runs arms over it, and reports.
//
//	ambit corpus [-dir corpus]
//	ambit verify [-dir corpus]
//	ambit run -arm b0|b|b-hosted|a-local|a-hosted [-limit n] [-llama url]
//	ambit report [-results results]
//	ambit state <record-id>   print the Arm A serialized state
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/scttfrdmn/ambit/internal/cedarq"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/gen"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ambit corpus|verify|run|report [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "corpus":
		err = cmdCorpus(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "state":
		err = cmdState(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ambit:", err)
		os.Exit(1)
	}
}

func cmdCorpus(args []string) error {
	fs := flag.NewFlagSet("corpus", flag.ExitOnError)
	dir := fs.String("dir", "corpus", "corpus directory (policies/ must already exist)")
	fs.Parse(args)

	pol, err := cedarq.LoadPolicies(*dir)
	if err != nil {
		return err
	}
	items, err := gen.Generate(gen.G0)
	if err != nil {
		return err
	}
	if err := gen.CrossCheck(pol, items); err != nil {
		return err
	}
	recs := make([]corpus.Record, len(items))
	truths := make([]corpus.Truth, len(items))
	for i, it := range items {
		recs[i], truths[i] = it.Record, it.Truth
	}
	if err := corpus.WriteJSONL(filepath.Join(*dir, corpus.RecordsFile), recs); err != nil {
		return err
	}
	if err := corpus.WriteJSONL(filepath.Join(*dir, corpus.OracleFile), truths); err != nil {
		return err
	}
	hash, err := corpus.WriteSums(*dir)
	if err != nil {
		return err
	}
	summarize(items)
	fmt.Printf("\ncross-check: cedar agrees with the oracle on %d/%d records, with true facts and with none\n", len(items), len(items))
	fmt.Printf("law 2: all injected records deny\ncorpus hash: %s\n", hash)
	return nil
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dir := fs.String("dir", "corpus", "corpus directory")
	fs.Parse(args)
	hash, err := corpus.Verify(*dir)
	if err != nil {
		return err
	}
	fmt.Println(hash)
	return nil
}

func summarize(items []gen.Item) {
	count := func(title string, key func(gen.Item) string) {
		m := map[string][2]int{}
		for _, it := range items {
			c := m[key(it)]
			if it.Truth.Verdict == corpus.Allow {
				c[0]++
			} else {
				c[1]++
			}
			m[key(it)] = c
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("\n%-24s %6s %6s\n", title, "allow", "deny")
		for _, k := range keys {
			fmt.Printf("%-24s %6d %6d\n", k, m[k][0], m[k][1])
		}
	}
	count("kind", func(it gen.Item) string {
		k := it.Truth.Kind
		if it.Truth.Injection != "" {
			k += "/" + it.Truth.Injection
		}
		if it.Truth.Variant != "" {
			k += "/" + it.Truth.Variant
		}
		return k
	})
	count("policy", func(it gen.Item) string { return fmt.Sprintf("p%d", it.Truth.Policy) })
	count("class", func(it gen.Item) string { return it.Truth.Class })
	count("payload", func(it gen.Item) string { return it.Record.Payload.Kind })
}
