// Package cedarq turns an invocation record's facts into an LKI pdp query.
// Every Cedar evaluation in ambit (the cross-check, Arm B0, Arm B) builds its
// query here, so the same facts reach every evaluation (Law 3).
package cedarq

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/lki/pdp"
	"github.com/scttfrdmn/ambit/internal/oracle"
)

// Query builds the Cedar request. s carries whatever semantic facts the
// caller has (none for B0, the PIP's for B, the truth for the cross-check);
// nil fields are left out of the context, never sent as nil.
func Query(f corpus.Facts, s corpus.Semantic) pdp.Query {
	roles := make([]string, len(f.Chain))
	for i, p := range f.Chain {
		roles[i] = p.Role
	}
	actor := f.Chain[len(f.Chain)-1]

	// Cedar skips a forbid whose condition errors, so every attribute a
	// policy reads must be present. Missing registry values become "none"
	// or an empty set.
	phi := f.Resource.PHI
	if phi == "" {
		phi = "none"
	}
	docKind := f.Resource.DocKind
	if docKind == "" {
		docKind = "none"
	}
	purposes := f.Resource.ApprovedPurposes
	if purposes == nil {
		purposes = []string{}
	}

	ctx := map[string]any{
		"purpose":     f.Purpose,
		"chain_roles": roles,
		"depth":       int64(f.Depth),
		"est_cost":    f.EstCost,
		"task_spent":  f.TaskSpent,
		"task_cap":    f.TaskCap,
		"swarm_spent": f.SwarmSpent,
		"swarm_cap":   f.SwarmCap,
	}
	if s.SQLTouchesIdentified != nil {
		ctx[oracle.FactSQLTouchesIdentified] = *s.SQLTouchesIdentified
	}
	if s.PayloadContainsIdentified != nil {
		ctx[oracle.FactPayloadContainsIdentified] = *s.PayloadContainsIdentified
	}
	if s.DocOnSubject != nil {
		ctx[oracle.FactDocOnSubject] = *s.DocOnSubject
	}

	return pdp.Query{
		Principal: pdp.Entity{
			Type:       "Ambit::Agent",
			ID:         actor.ID,
			Attributes: map[string]any{"role": actor.Role},
		},
		Action: fmt.Sprintf("Ambit::Action::%q", f.Tool),
		Resource: pdp.Entity{
			Type: "Ambit::" + f.Resource.Type,
			ID:   f.Resource.ID,
			Attributes: map[string]any{
				"phi":               phi,
				"doc_kind":          docKind,
				"approved_purposes": purposes,
			},
		},
		Context: ctx,
	}
}

// LoadPolicies reads the frozen Cedar policy set from a corpus directory.
func LoadPolicies(corpusDir string) (*pdp.Policies, error) {
	b, err := os.ReadFile(filepath.Join(corpusDir, corpus.PolicyDir, corpus.CedarFile))
	if err != nil {
		return nil, err
	}
	return pdp.LoadPolicies(string(b))
}

// Evaluate runs one query and fails on any Cedar evaluation error: a forbid
// that errors is skipped by Cedar, which would fail open.
func Evaluate(p *pdp.Policies, q pdp.Query) (pdp.Decision, error) {
	d, err := pdp.Evaluate(nil, p, q)
	if err != nil {
		return d, err
	}
	if len(d.Diagnostics) > 0 {
		return d, fmt.Errorf("cedar evaluation errors: %v", d.Diagnostics)
	}
	// cedar-go reports determining policies in map order; sort them so that
	// decisions and quill entries are reproducible.
	slices.Sort(d.DeterminingPolicies)
	return d, nil
}
