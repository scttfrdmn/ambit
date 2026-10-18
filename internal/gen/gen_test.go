package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/scttfrdmn/ambit/internal/cedarq"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/lki/pdp"
)

const corpusDir = "../../corpus"

func generate(t *testing.T) []Item {
	t.Helper()
	items, err := Generate(G0)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestDeterministic(t *testing.T) {
	if !reflect.DeepEqual(generate(t), generate(t)) {
		t.Fatal("two runs with the same seed differ")
	}
}

func TestSizeAndLaw2(t *testing.T) {
	items := generate(t)
	if len(items) != 1000 {
		t.Fatalf("got %d records, want 1000", len(items))
	}
	for _, it := range items {
		if it.Truth.Kind == corpus.KindInjected && it.Truth.Verdict != corpus.Deny {
			t.Errorf("law 2: %s injected but %s", it.Record.ID, it.Truth.Verdict)
		}
	}
}

func TestCrossCheckAgrees(t *testing.T) {
	p, err := cedarq.LoadPolicies(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := CrossCheck(p, generate(t)); err != nil {
		t.Fatal(err)
	}
}

// TestCrossCheckCatchesDivergence confirms the cross-check can fail: each
// mutation makes the Cedar text disagree with the prose in one place, and
// the corpus must contain a record that exposes it (Law 5).
func TestCrossCheckCatchesDivergence(t *testing.T) {
	src, err := os.ReadFile(corpusDir + "/policies/ambit.cedar")
	if err != nil {
		t.Fatal(err)
	}
	items := generate(t)
	mutations := map[string][2]string{
		"swarm cap boundary":   {"context.swarm_spent + context.est_cost > context.swarm_cap", "context.swarm_spent + context.est_cost >= context.swarm_cap"},
		"ra cannot delegate":   {`unless { ["pi", "analyst", "research_assistant"].containsAll(context.chain_roles) };` + "\n\n// ---- Policy 5", `unless { ["pi", "analyst"].containsAll(context.chain_roles) };` + "\n\n// ---- Policy 5"},
		"allowlist actor only": {`unless { ["pi", "analyst"].containsAll(context.chain_roles) };` + "\n\n@id(\"p4.allowlist.delegate\")", `unless { ["pi", "analyst"].contains(principal.role) };` + "\n\n@id(\"p4.allowlist.delegate\")"},
		"cap boundary":         {"context.task_spent + context.est_cost > context.task_cap", "context.task_spent + context.est_cost >= context.task_cap"},
		"swarm cap dropped":    {"context.swarm_spent + context.est_cost > context.swarm_cap", "false"},
		"depth off by one":     {"when { context.depth > 3 }", "when { context.depth > 4 }"},
		"delegate depth":       {"context.depth + 1 > 3", "context.depth > 3"},
		"identified roles":     {`!(["pi", "analyst"].containsAll(context.chain_roles))`, `!(["pi", "analyst", "research_assistant"].containsAll(context.chain_roles))`},
		"purpose rule":         {"!resource.approved_purposes.contains(context.purpose)", "false"},
		"fail open on sql":     {"(!(context has sql_touches_identified) || context.sql_touches_identified)", "(context has sql_touches_identified && context.sql_touches_identified)"},
		"egress dropped":       {"!(context has payload_contains_identified) || context.payload_contains_identified", "false"},
		"subject inverted":     {"!context.doc_on_subject)", "context.doc_on_subject)"},
		"compute allowlist":    {`unless { ["pi", "analyst"].containsAll(context.chain_roles) };` + "\n\n@id(\"p4.allowlist.delegate\")", `unless { ["pi", "analyst", "research_assistant"].containsAll(context.chain_roles) };` + "\n\n@id(\"p4.allowlist.delegate\")"},
		"web_fetch allowed":    {`@id("p2.egress.no_web_fetch")` + "\n" + `forbid (principal, action == Ambit::Action::"web_fetch", resource);`, ""},
		"unknown tool allowed": {`Ambit::Action::"delegate"` + "\n  ],", `Ambit::Action::"delegate", Ambit::Action::"shell_exec", Ambit::Action::"s3_put_object", Ambit::Action::"install_package", Ambit::Action::"modify_iam_policy", Ambit::Action::"browser_automation"` + "\n  ],"},
	}
	for name, m := range mutations {
		if !strings.Contains(string(src), m[0]) {
			t.Fatalf("%s: pattern not found in policy text", name)
		}
		p, err := pdp.LoadPolicies(strings.Replace(string(src), m[0], m[1], 1))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := CrossCheck(p, items); err == nil {
			t.Errorf("%s: cross-check passed on mutated policies", name)
		}
	}
}

// TestB0FailsClosed: without semantic facts, Cedar denies every margin
// record and agrees with the oracle on every within-ambit record.
func TestB0FailsClosed(t *testing.T) {
	p, err := cedarq.LoadPolicies(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range generate(t) {
		d, err := cedarq.Evaluate(p, cedarq.Query(it.Record.Facts, corpus.Semantic{}))
		if err != nil {
			t.Fatal(err)
		}
		switch it.Truth.Class {
		case corpus.ClassMargin:
			if d.Allowed {
				t.Errorf("%s: margin record allowed without facts", it.Record.ID)
			}
		default:
			if d.Allowed != (it.Truth.Verdict == corpus.Allow) {
				t.Errorf("%s: %s record, oracle %s, B0 allowed=%v", it.Record.ID, it.Truth.Class, it.Truth.Verdict, d.Allowed)
			}
		}
	}
}

// TestFrozenCorpusMatchesGenerator: the committed corpus is byte for byte
// what the generator produces, and its sums verify.
func TestFrozenCorpusMatchesGenerator(t *testing.T) {
	if _, err := corpus.Verify(corpusDir); err != nil {
		t.Fatal(err)
	}
	items := generate(t)
	recs := make([]corpus.Record, len(items))
	truths := make([]corpus.Truth, len(items))
	for i, it := range items {
		recs[i], truths[i] = it.Record, it.Truth
	}
	tmp := t.TempDir()
	if err := corpus.WriteJSONL(filepath.Join(tmp, corpus.RecordsFile), recs); err != nil {
		t.Fatal(err)
	}
	if err := corpus.WriteJSONL(filepath.Join(tmp, corpus.OracleFile), truths); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{corpus.RecordsFile, corpus.OracleFile} {
		want, err := os.ReadFile(filepath.Join(tmp, f))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(corpusDir, f))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frozen %s differs from the generator; run make corpus", f)
		}
	}
}
