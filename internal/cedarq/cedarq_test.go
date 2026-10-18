package cedarq

import (
	"slices"
	"testing"

	"github.com/scttfrdmn/ambit/internal/corpus"
)

func facts(tool string) corpus.Facts {
	return corpus.Facts{
		Tool:     tool,
		Chain:    []corpus.Principal{{ID: "agent:root", Role: "pi"}},
		Purpose:  "pcsk9_trial_analysis",
		Resource: corpus.Resource{Type: "Url", ID: "https://example.org"},
		EstCost:  10, TaskCap: 100, SwarmCap: 1000,
	}
}

func TestPoliciesKeyedByID(t *testing.T) {
	p, err := LoadPolicies("../../corpus")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Evaluate(p, Query(facts("web_fetch"), corpus.Semantic{}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || !slices.Equal(d.DeterminingPolicies, []string{"p2.egress.no_web_fetch"}) {
		t.Fatalf("web_fetch: got %+v", d)
	}
	d, err = Evaluate(p, Query(facts("run_compute"), corpus.Semantic{}))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed || !slices.Equal(d.DeterminingPolicies, []string{"baseline.known_tools"}) {
		t.Fatalf("run_compute: got %+v", d)
	}
	d, err = Evaluate(p, Query(facts("shell_exec"), corpus.Semantic{}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || len(d.DeterminingPolicies) != 0 {
		t.Fatalf("shell_exec: got %+v", d)
	}
}
