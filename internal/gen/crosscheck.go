package gen

import (
	"fmt"
	"slices"

	"github.com/scttfrdmn/ambit/internal/cedarq"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/lki/pdp"
	"github.com/scttfrdmn/ambit/internal/oracle"
)

// CrossCheck evaluates every record in Cedar twice: with its true semantic
// facts, and with none (as B0 sees it). Each time Cedar must agree with the
// oracle on the verdict and on the determining policies. A disagreement
// means the Cedar text and the prose (as the oracle implements it) have
// diverged: a corpus defect (Law 1).
func CrossCheck(p *pdp.Policies, items []Item) error {
	var bad []string
	for _, it := range items {
		for _, s := range []corpus.Semantic{it.Truth.Semantic, {}} {
			d, err := cedarq.Evaluate(p, cedarq.Query(it.Record.Facts, s))
			if err != nil {
				return fmt.Errorf("%s: %w", it.Record.ID, err)
			}
			verdict := oracle.Verdict(it.Record.Facts, s)
			want := oracle.Violations(it.Record.Facts, s)
			if want == nil {
				want = []string{}
			}
			if verdict == corpus.Allow {
				want = []string{oracle.RuleKnownTools}
			}
			got := slices.Sorted(slices.Values(d.DeterminingPolicies))
			if got == nil {
				got = []string{}
			}
			if d.Allowed != (verdict == corpus.Allow) || !slices.Equal(got, want) {
				bad = append(bad, fmt.Sprintf("%s (semantic %s): oracle %s %v, cedar allowed=%v %v",
					it.Record.ID, describe(s), verdict, want, d.Allowed, got))
			}
		}
	}
	if len(bad) > 0 {
		if len(bad) > 20 {
			bad = append(bad[:20], fmt.Sprintf("... and %d more", len(bad)-20))
		}
		return fmt.Errorf("cedar disagrees with the oracle on %d records:\n  %s", len(bad), joinLines(bad))
	}
	return nil
}

func joinLines(xs []string) string {
	s := ""
	for i, x := range xs {
		if i > 0 {
			s += "\n  "
		}
		s += x
	}
	return s
}

func describe(s corpus.Semantic) string {
	if s == (corpus.Semantic{}) {
		return "none"
	}
	return "true"
}
