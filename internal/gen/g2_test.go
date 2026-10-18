package gen

import (
	"strings"
	"testing"

	"github.com/scttfrdmn/ambit/internal/cedarq"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/vocab"
)

// small is a G2-shaped configuration small enough for unit tests.
var small = Config{Seed: 7, Families: 200, BasePerFamily: 8, Injected: 300, Variants: 300,
	HeaderPairs: 150, Traces: TraceConfig{Cost: 20, Delegation: 20, Relay: 20}}

func TestG2Stages(t *testing.T) {
	items, err := Generate(small)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != small.Size() {
		t.Fatalf("got %d records, config says %d", len(items), small.Size())
	}
	p, err := cedarq.LoadPolicies(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := CrossCheck(p, items); err != nil {
		t.Fatal(err)
	}
	byID := map[string]Item{}
	for _, it := range items {
		byID[it.Record.ID] = it
	}
	traces := map[string][]Item{}
	for _, it := range items {
		tr := it.Truth
		if tr.Injection != "" && tr.Verdict != corpus.Deny {
			t.Errorf("law 2: %s carries an injection but is %s", tr.ID, tr.Verdict)
		}
		switch tr.Variant {
		case VariantHeaderAdded, VariantHeaderRemoved:
			base := byID[tr.BaseID]
			if tr.Verdict != base.Truth.Verdict || tr.Verdict != corpus.Allow {
				t.Errorf("header twin %s: verdict %s, base %s", tr.ID, tr.Verdict, base.Truth.Verdict)
			}
			if it.Record.Facts.Tool == vocab.ToolDelegate {
				t.Errorf("header twin %s is a delegate call", tr.ID)
			}
			has := strings.Contains(it.Record.Payload.Text, "Patient: ")
			if has != (tr.Variant == VariantHeaderAdded) {
				t.Errorf("header twin %s (%s): header present = %v", tr.ID, tr.Variant, has)
			}
		}
		if tr.Kind == corpus.KindTrace {
			traces[tr.Trace] = append(traces[tr.Trace], it)
		}
	}
	for id, steps := range traces {
		at := map[int]Item{}
		for _, s := range steps {
			at[s.Truth.Step] = s
		}
		switch steps[0].Truth.Scenario {
		case "trace_delegation":
			for d := 0; d+1 < len(steps); d++ {
				child, next := at[d].Record.Facts.Params, at[d+1].Record.Facts.Chain
				caller := next[len(next)-1]
				if child["agent_id"] != caller.ID || child["role"] != caller.Role {
					t.Errorf("trace %s: step %d delegates to %v/%v, step %d caller is %s/%s",
						id, d, child["agent_id"], child["role"], d+1, caller.ID, caller.Role)
				}
			}
		case "trace_cost":
			spent := at[0].Record.Facts.SwarmSpent
			for s := 0; s < len(steps); s++ {
				f := at[s].Record.Facts
				if f.SwarmSpent != spent {
					t.Errorf("trace %s step %d: swarm spent %d, want %d", id, s, f.SwarmSpent, spent)
				}
				if at[s].Truth.Verdict == corpus.Allow {
					spent += f.EstCost
				}
			}
		case "trace_relay":
			for h := 0; h+1 < len(steps); h++ {
				if at[h].Record.Facts.Destination.ID != at[h+1].Record.Facts.Chain[0].ID {
					t.Errorf("trace %s: hop %d recipient is not hop %d sender", id, h, h+1)
				}
			}
		}
	}
}
