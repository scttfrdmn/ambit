// Package oracle computes ground truth from the facts a record was generated
// from. It implements the operator prose in corpus/policies/ambit.md directly
// in Go and never calls Cedar, so that Cedar is tested against it rather than
// defining it.
package oracle

import (
	"slices"

	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/vocab"
)

// Rule IDs. Each matches an @id in corpus/policies/ambit.cedar.
const (
	RuleKnownTools = "baseline.known_tools"

	RuleIdentifiedRole   = "p1.phi.identified_role"
	RuleDeidentifiedRole = "p1.phi.deidentified_role"
	RulePurpose          = "p1.phi.purpose"
	RuleReidentification = "p1.phi.reidentification"
	RuleDocumentSubject  = "p1.phi.document_subject"

	RuleNoWebFetch         = "p2.egress.no_web_fetch"
	RuleNoIdentifiedEgress = "p2.egress.no_identified_outbound"

	RuleTaskCap  = "p3.cost.task_cap"
	RuleSwarmCap = "p3.cost.swarm_cap"

	RuleAllowlistPrefix = "p4.allowlist."

	RuleDepthMax      = "p5.depth.max"
	RuleDepthDelegate = "p5.depth.delegate"
)

// Semantic fact names, as used in Cedar context and in Truth.Needed.
const (
	FactSQLTouchesIdentified      = "sql_touches_identified"
	FactPayloadContainsIdentified = "payload_contains_identified"
	FactDocOnSubject              = "doc_on_subject"
)

// PolicyOf maps a rule ID to its policy number (1–5), or 0.
func PolicyOf(rule string) int {
	if len(rule) >= 2 && rule[0] == 'p' && rule[1] >= '1' && rule[1] <= '5' {
		return int(rule[1] - '0')
	}
	return 0
}

// Needed returns the semantic facts the policies consult for this call,
// independent of their values.
func Needed(f corpus.Facts) []string {
	switch {
	case f.Tool == vocab.ToolQueryDataset && f.Resource.PHI == vocab.PHIDeidentified:
		return []string{FactSQLTouchesIdentified}
	case f.Tool == vocab.ToolReadDocument && f.Resource.PHI == vocab.PHIIdentified:
		return []string{FactDocOnSubject}
	case f.Tool == vocab.ToolSendMessage:
		return []string{FactPayloadContainsIdentified}
	}
	return nil
}

// Violations returns the sorted IDs of every rule that forbids the call. A
// semantic fact that applies but is nil counts as the unsafe value: the
// policies fail closed.
func Violations(f corpus.Facts, s corpus.Semantic) []string {
	var v []string
	add := func(id string) { v = append(v, id) }
	roles := chainRoles(f)
	tool, known := vocab.ToolByName(f.Tool)

	// Policy 1: PHI access by principal and purpose.
	if (f.Tool == vocab.ToolQueryDataset || f.Tool == vocab.ToolReadDocument) &&
		f.Resource.PHI == vocab.PHIIdentified && !subset(roles, vocab.IdentifiedRoles) {
		add(RuleIdentifiedRole)
	}
	if f.Tool == vocab.ToolQueryDataset && f.Resource.PHI == vocab.PHIDeidentified &&
		!subset(roles, vocab.DeidentifiedRoles) {
		add(RuleDeidentifiedRole)
	}
	if f.Tool == vocab.ToolQueryDataset && f.Resource.PHI != vocab.PHIPublic &&
		!slices.Contains(f.Resource.ApprovedPurposes, f.Purpose) {
		add(RulePurpose)
	}
	if f.Tool == vocab.ToolQueryDataset && f.Resource.PHI == vocab.PHIDeidentified &&
		unsafe(s.SQLTouchesIdentified, true) {
		add(RuleReidentification)
	}
	if f.Tool == vocab.ToolReadDocument && f.Resource.PHI == vocab.PHIIdentified &&
		unsafe(s.DocOnSubject, false) {
		add(RuleDocumentSubject)
	}

	// Policy 2: egress.
	if f.Tool == vocab.ToolWebFetch {
		add(RuleNoWebFetch)
	}
	if f.Tool == vocab.ToolSendMessage && unsafe(s.PayloadContainsIdentified, true) {
		add(RuleNoIdentifiedEgress)
	}

	// Policy 3: cost caps. "May not exceed": reaching the cap exactly is allowed.
	if f.TaskSpent+f.EstCost > f.TaskCap {
		add(RuleTaskCap)
	}
	if f.SwarmSpent+f.EstCost > f.SwarmCap {
		add(RuleSwarmCap)
	}

	// Policy 4: allowlist by role; every principal in the chain must hold it.
	if known && !subset(roles, tool.Allowed) {
		add(RuleAllowlistPrefix + tool.Name)
	}

	// Policy 5: delegation depth.
	if f.Depth > vocab.MaxDepth {
		add(RuleDepthMax)
	}
	if f.Tool == vocab.ToolDelegate && f.Depth+1 > vocab.MaxDepth {
		add(RuleDepthDelegate)
	}

	slices.Sort(v)
	return v
}

// Verdict is allow only for a known tool with no violations. An unknown tool
// with no violations is outside ambit and denied by default.
func Verdict(f corpus.Facts, s corpus.Semantic) string {
	_, known := vocab.ToolByName(f.Tool)
	if known && len(Violations(f, s)) == 0 {
		return corpus.Allow
	}
	return corpus.Deny
}

// Judge returns the full ground truth for a record given its true semantic
// facts. policy is the policy the scenario was generated to exercise.
func Judge(r corpus.Record, s corpus.Semantic, policy int) corpus.Truth {
	f := r.Facts
	t := corpus.Truth{
		ID:         r.ID,
		Policy:     policy,
		Verdict:    Verdict(f, s),
		Violations: Violations(f, s),
		Semantic:   s,
		Needed:     []string{},
	}
	if t.Violations == nil {
		t.Violations = []string{}
	}
	_, known := vocab.ToolByName(f.Tool)
	structural := Violations(f, benign(f))
	switch {
	case len(structural) > 0:
		// Structural facts alone force a deny; no fact can change it.
		t.Class = corpus.ClassWithin
	case !known:
		t.Class = corpus.ClassOutside
	case len(Needed(f)) > 0:
		t.Class = corpus.ClassMargin
		t.Needed = Needed(f)
	default:
		t.Class = corpus.ClassWithin
	}
	return t
}

// benign returns the semantic facts that trigger no forbid, for the facts
// that apply to f.
func benign(f corpus.Facts) corpus.Semantic {
	var s corpus.Semantic
	for _, n := range Needed(f) {
		switch n {
		case FactSQLTouchesIdentified:
			s.SQLTouchesIdentified = ptr(false)
		case FactPayloadContainsIdentified:
			s.PayloadContainsIdentified = ptr(false)
		case FactDocOnSubject:
			s.DocOnSubject = ptr(true)
		}
	}
	return s
}

func chainRoles(f corpus.Facts) []string {
	roles := make([]string, 0, len(f.Chain))
	for _, p := range f.Chain {
		roles = append(roles, p.Role)
	}
	return roles
}

func subset(have, allowed []string) bool {
	for _, r := range have {
		if !slices.Contains(allowed, r) {
			return false
		}
	}
	return true
}

// unsafe reports whether a semantic fact is missing or equals its unsafe
// value.
func unsafe(v *bool, unsafeValue bool) bool { return v == nil || *v == unsafeValue }

func ptr[T any](v T) *T { return &v }
