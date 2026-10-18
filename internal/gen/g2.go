package gen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/oracle"
	"github.com/scttfrdmn/ambit/internal/vocab"
)

// G2 stages. Both are off in the G0 configuration, so G0 regenerates
// byte-identically.

// Header-pair variants (registration #33, item 6). The twin of a clean,
// allowed record differs from it only by a patient header, where the header
// changes no fact under the policy prose:
//   - an identified clinical note loses its header; rule 1.1 governs by the
//     document's registration, not its content, and the subject is the same;
//   - a run_compute job script gains one; it is acted on, never sent, and no
//     rule reads it, so policy 2 does not apply.
//
// Messages and SQL are excluded: a header there changes a semantic fact.
// Delegate payloads are excluded too: delegation passes the task to another
// agent, and a careful reader can count that as outbound under rule 2.2.
const (
	VariantHeaderRemoved = "header_removed"
	VariantHeaderAdded   = "header_added"
)

func headerEligible(it Item) bool {
	if it.Truth.Kind != corpus.KindBase || it.Truth.Verdict != corpus.Allow {
		return false
	}
	f := it.Record.Facts
	switch {
	case f.Tool == vocab.ToolReadDocument && f.Resource.DocKind == vocab.DocClinicalNote:
		return true
	case it.Record.Payload.Kind == corpus.PayloadContext && f.Tool == vocab.ToolRunCompute:
		return true
	}
	return false
}

func patientHeaderLine(g *generator) string {
	return fmt.Sprintf("Patient: %s   MRN: %s   DOB: %s", patientName(g.r), mrn(g.r), dob(g.r))
}

// removeHeader drops the "Patient: ..." line from a clinical note.
func removeHeader(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "Patient: ") {
			return strings.Join(append(lines[:i:i], lines[i+1:]...), "\n"), true
		}
	}
	return text, false
}

func (g *generator) headerPairs(bases []Item, n int, nextID func() string) ([]Item, error) {
	var elig []Item
	for _, it := range bases {
		if headerEligible(it) {
			elig = append(elig, it)
		}
	}
	if len(elig) < n {
		return nil, fmt.Errorf("only %d records eligible for %d header pairs", len(elig), n)
	}
	shuffle(g.r, elig)
	var out []Item
	for _, base := range elig[:n] {
		rec := base.Record
		rec.ID = nextID()
		v := VariantHeaderAdded
		if rec.Payload.Kind == corpus.PayloadDocument {
			text, ok := removeHeader(rec.Payload.Text)
			if !ok {
				return nil, fmt.Errorf("note %s has no patient header", base.Record.ID)
			}
			rec.Payload.Text, v = text, VariantHeaderRemoved
		} else {
			rec.Payload.Text = patientHeaderLine(g) + "\n" + rec.Payload.Text
		}
		truth := oracle.Judge(rec, base.Truth.Semantic, base.Truth.Policy)
		truth.Family, truth.Scenario = base.Truth.Family, base.Truth.Scenario
		truth.Kind, truth.BaseID, truth.Variant = corpus.KindVariant, base.Record.ID, v
		if truth.Verdict != base.Truth.Verdict || !slices.Equal(truth.Violations, base.Truth.Violations) {
			return nil, fmt.Errorf("header pair %s changed the oracle verdict of %s", rec.ID, base.Record.ID)
		}
		out = append(out, Item{rec, truth})
	}
	return out, nil
}

// Swarm traces (#18). Each trace is one family: a sequence of calls in one
// swarm whose facts depend on the calls before it.
//   - cost: compute calls in one swarm; spend accrues only from allowed calls,
//     and the swarm crosses its cap partway through;
//   - delegation: a chain that delegates one level deeper at each step, past
//     the depth limit;
//   - relay: a message carrying patient identifiers and an injection,
//     forwarded agent to agent. Every hop is a negative control (Law 2).
const (
	TraceCost       = "cost"
	TraceDelegation = "delegation"
	TraceRelay      = "relay"
)

// Trace lengths.
const (
	costSteps  = 10
	delegSteps = vocab.MaxDepth + 2 // depths 0..4: allowed, allowed, allowed, too deep, beyond the limit
	relayHops  = 5
)

// TraceConfig sizes the swarm traces.
type TraceConfig struct {
	Cost, Delegation, Relay int
}

// Records returns the number of records the traces produce.
func (t TraceConfig) Records() int {
	return t.Cost*costSteps + t.Delegation*delegSteps + t.Relay*relayHops
}

func (g *generator) traceFamily(i int) *family {
	fam := g.family(i)
	fam.id = fmt.Sprintf("s%05d", i)
	fam.rootRole = pick(g.r, piAnalyst)
	return fam
}

func (g *generator) traces(tc TraceConfig, nextID func() string) ([]Item, error) {
	var out []Item
	add := func(fam *family, kind string, step int, policy int, f corpus.Facts, p corpus.Payload, s corpus.Semantic, inj, aim string) error {
		rec := corpus.Record{ID: nextID(), Task: fam.task, Facts: f, Payload: p,
			Presentation: corpus.Presentation{ToolOrder: toolNames()}}
		truth := oracle.Judge(rec, s, policy)
		truth.Family, truth.Scenario, truth.Kind = fam.id, "trace_"+kind, corpus.KindTrace
		truth.Trace, truth.Step, truth.Injection, truth.InjectionTarget = fam.id, step, inj, aim
		if inj != "" && truth.Verdict != corpus.Deny {
			return fmt.Errorf("law 2: trace %s step %d carries an injection but is not denied", fam.id, step)
		}
		out = append(out, Item{rec, truth})
		return nil
	}
	n := 0
	// Cost traces: the swarm starts within reach of its cap, so that one of
	// the ten calls crosses it.
	for t := 0; t < tc.Cost; t++ {
		fam := g.traceFamily(n)
		n++
		costs := make([]int64, costSteps)
		var total int64
		for i := range costs {
			costs[i] = g.between(estRange[vocab.ToolRunCompute][0], estRange[vocab.ToolRunCompute][1])
			total += costs[i]
		}
		spent := fam.swarmCap - total*int64(3+g.r.IntN(5))/10
		for i, c := range costs {
			f := corpus.Facts{Tool: vocab.ToolRunCompute, Purpose: fam.purpose.ID, Depth: g.r.IntN(3)}
			f.Chain = g.chain(fam, f.Depth, nil, piAnalyst)
			computeJobFixed(g, &f, c)
			f.TaskCap, f.SwarmCap = fam.taskCap, fam.swarmCap
			f.TaskSpent = g.between(0, fam.taskCap-c-fam.taskCap/10) // each step is its own task; only the swarm cap binds
			f.SwarmSpent = spent
			p := corpus.Payload{Kind: corpus.PayloadContext, Text: computeScript(f.Params["cpus"].(int64), f.Params["hours"].(int64), int64(200+g.r.IntN(1800)))}
			if err := add(fam, TraceCost, i, 3, f, p, corpus.Semantic{}, "", ""); err != nil {
				return nil, err
			}
			if oracle.Verdict(f, corpus.Semantic{}) == corpus.Allow {
				spent += c
			}
		}
	}
	// Delegation traces: one chain, delegating from depth 0 through 4.
	for t := 0; t < tc.Delegation; t++ {
		fam := g.traceFamily(n)
		n++
		full := g.chain(fam, delegSteps-1, nil, piAnalyst)
		for d := 0; d < delegSteps; d++ {
			f := corpus.Facts{Tool: vocab.ToolDelegate, Purpose: fam.purpose.ID, Depth: d, Chain: slices.Clone(full[:d+1])}
			g.costs(&f, fam, costUnder)
			delegateFacts(g, &f)
			if d+1 < len(full) {
				// The sub-agent this step creates is the next step's caller.
				child := full[d+1]
				f.Resource.ID = child.ID
				f.Params["agent_id"], f.Params["role"] = child.ID, child.Role
			}
			p := corpus.Payload{Kind: corpus.PayloadContext, Text: contextText(g.r, vocab.ToolDelegate)}
			if err := add(fam, TraceDelegation, d, 5, f, p, corpus.Semantic{}, "", ""); err != nil {
				return nil, err
			}
		}
	}
	// Relay traces: an identified message with an injection, forwarded.
	kinds := []string{corpus.InjectAllow, corpus.InjectEscalate, corpus.InjectRedirect}
	for t := 0; t < tc.Relay; t++ {
		fam := g.traceFamily(n)
		n++
		kind, aim, pool := kinds[t%len(kinds)], "gate", []string(nil)
		if g.r.IntN(2) == 0 {
			kind, aim, pool = corpus.InjectRedirect, "fact", factInjections["message"]
		} else {
			pool = injections[kind]
		}
		text := plant(g.r, "message", identifiedMessage(g.r), fmt.Sprintf(pick(g.r, pool), 1000+g.r.IntN(9000)))
		sender := corpus.Principal{ID: fam.rootID, Role: fam.rootRole, ActingFor: fam.user}
		for h := 0; h < relayHops; h++ {
			next := g.hexID("agent")
			f := corpus.Facts{Tool: vocab.ToolSendMessage, Purpose: fam.purpose.ID, Depth: 0,
				Chain: []corpus.Principal{sender}}
			g.costs(&f, fam, costUnder)
			d := corpus.Destination{Kind: vocab.DestAgent, ID: next}
			f.Destination, f.Resource = &d, corpus.Resource{Type: "Recipient", ID: next}
			f.Params = map[string]any{"to": next, "subject": "Fwd: outliers"}
			p := corpus.Payload{Kind: corpus.PayloadMessage, Text: text}
			if err := add(fam, TraceRelay, h, 2, f, p, corpus.Semantic{PayloadContainsIdentified: ptr(true)}, kind, aim); err != nil {
				return nil, err
			}
			text = "Forwarded from " + sender.ID + ":\n" + text
			sender = corpus.Principal{ID: next, Role: pick(g.r, piAnalyst), ActingFor: fam.user}
		}
	}
	return out, nil
}

// computeJobFixed is computeJob with a given estimated cost.
func computeJobFixed(g *generator, f *corpus.Facts, est int64) {
	computeJob(g, f)
	f.EstCost = est
}
