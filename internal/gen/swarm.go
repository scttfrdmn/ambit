package gen

import (
	"fmt"
	"math/rand/v2"
	"slices"

	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/vocab"
)

// Live-swarm calls (G3, #21). The swarm draws each agent's tool calls with
// the corpus's own scenario builders, so a live call carries the same kinds
// of parameters, resources, and payloads as a corpus record. It keeps a
// call's content (tool, parameters, resource, destination, estimated cost,
// payload, and the semantic facts the payload was written from) and drops
// what the runtime supplies live: the principal chain, the depth, the spend,
// and the caps.
//
// Nothing here runs in Generate, so corpus output is unchanged.

// swarmScenarios are the scenarios a swarm agent's work calls are drawn
// from. Delegation is the swarm's own structure, and cost and depth limits
// are crossed live, so scenarios that fix a chain, a depth, or a spend are
// left out.
var swarmScenarios = []string{
	"query_public", "query_identified_ok", "query_deid_clean", "read_note_on_subject", "read_literature",
	"identified_wrong_role", "purpose_mismatch", "reidentification", "read_note_off_subject",
	"web_fetch", "send_clean", "send_identified", "compute_under_cap", "role_not_allowlisted", "unknown_tool",
}

// SwarmTask is one task of a live swarm: a root coordinator, the user it
// acts for, a purpose, and a task cap, drawn as a corpus family is.
type SwarmTask struct {
	RootID   string `json:"root_id"`
	RootRole string `json:"root_role"`
	User     string `json:"user"`
	Text     string `json:"text"`
	Purpose  string `json:"purpose"`
	TaskCap  int64  `json:"task_cap_micros"`
}

// Call is the content of one live tool call. Facts holds the tool,
// parameters, purpose, resource, destination, and estimated cost; the chain,
// depth, spend, and caps are left zero for the runtime to fill.
type Call struct {
	Scenario string          `json:"scenario"`
	Policy   int             `json:"policy"`
	Facts    corpus.Facts    `json:"facts"`
	Payload  corpus.Payload  `json:"payload"`
	Semantic corpus.Semantic `json:"semantic"`
}

// Drawer draws swarm tasks, agents, and calls from one seeded stream.
type Drawer struct {
	g *generator
}

// NewDrawer returns a drawer seeded as Generate seeds the corpus. It draws
// on-subject notes from the #55-fixed templates (NoteRevision 1); no G3
// swarm ran on the original ones.
func NewDrawer(seed uint64) *Drawer {
	return &Drawer{g: &generator{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), noteRev: 1}}
}

// IntN draws from [0, n).
func (d *Drawer) IntN(n int) int { return d.g.r.IntN(n) }

// Weighted draws an index with the given weights.
func (d *Drawer) Weighted(w []int) int { return weighted(d.g.r, w) }

// AgentID draws an agent ID.
func (d *Drawer) AgentID() string { return d.g.hexID("agent") }

// Task draws the i'th task.
func (d *Drawer) Task(i int) SwarmTask {
	fam := d.g.family(i)
	return SwarmTask{RootID: fam.rootID, RootRole: fam.rootRole, User: fam.user, Text: fam.task,
		Purpose: fam.purpose.ID, TaskCap: fam.taskCap}
}

func (d *Drawer) family(t SwarmTask, role string) *family {
	p, _ := vocab.PurposeByID(t.Purpose)
	// The builders draw spend under the corpus's largest caps; the swarm
	// drops the spend, and the runtime supplies its own.
	return &family{rootID: t.RootID, purpose: p, rootRole: role, user: t.User, task: t.Text,
		taskCap: 60 * vocab.Micro, swarmCap: 800 * vocab.Micro}
}

// Work draws one work call for an agent with the given role, from the
// scenarios feasible for that role and the task's purpose, by scenario
// weight.
func (d *Drawer) Work(t SwarmTask, role string) Call {
	fam := d.family(t, role)
	var idx, w []int
	for i, tg := range targets {
		if !slices.Contains(swarmScenarios, tg.name) {
			continue
		}
		if tg.roots != nil && !slices.Contains(tg.roots, role) {
			continue
		}
		if tg.purposes != nil && !slices.Contains(tg.purposes, t.Purpose) {
			continue
		}
		idx = append(idx, i)
		w = append(w, tg.weight)
	}
	tg := targets[idx[weighted(d.g.r, w)]]
	f, p, s := tg.build(d.g, fam)
	return Call{Scenario: tg.name, Policy: tg.policy, Facts: content(f), Payload: p, Semantic: s}
}

// Delegate draws a delegate call that creates the given sub-agent.
func (d *Drawer) Delegate(t SwarmTask, childID, childRole string) Call {
	f := corpus.Facts{Tool: vocab.ToolDelegate, Purpose: t.Purpose}
	rg := estRange[vocab.ToolDelegate]
	f.EstCost = d.g.between(rg[0], rg[1])
	delegateFacts(d.g, &f)
	f.Resource.ID = childID
	f.Params["agent_id"], f.Params["role"] = childID, childRole
	p := corpus.Payload{Kind: corpus.PayloadContext, Text: contextText(d.g.r, vocab.ToolDelegate)}
	return Call{Scenario: "delegate", Policy: 5, Facts: f, Payload: p}
}

// content keeps a call's content and drops its runtime facts.
func content(f corpus.Facts) corpus.Facts {
	return corpus.Facts{Tool: f.Tool, Params: f.Params, Purpose: f.Purpose, Resource: f.Resource,
		Destination: f.Destination, EstCost: f.EstCost}
}

// ToolNames lists the known tools in vocabulary order, the presentation
// order of an unvaried record.
func ToolNames() []string { return toolNames() }

// SwarmTaskID names the i'th task.
func SwarmTaskID(i int) string { return fmt.Sprintf("t%05d", i) }
