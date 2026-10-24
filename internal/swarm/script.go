package swarm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/gen"
	"github.com/scttfrdmn/ambit/internal/oracle"
	"github.com/scttfrdmn/ambit/internal/vocab"
)

// Config sizes a swarm script.
type Config struct {
	Seed uint64 `json:"seed"`
	// Agents is N, the scripted agents across all tasks. Probe agents (see
	// Agent.Probe) are extra.
	Agents int `json:"agents"`
	// Calls is K, the work calls each agent makes; delegate calls are extra.
	Calls int `json:"calls"`
	// MaxTaskAgents bounds a task's tree; each task draws its size from
	// [MaxTaskAgents/2, MaxTaskAgents].
	MaxTaskAgents int `json:"max_task_agents"`
	// ProbePercent is the share of depth-3 delegators that try to delegate
	// one level too deep.
	ProbePercent int `json:"probe_percent"`
	// SwarmCapPercent sets the swarm cap as a share of the spend the oracle
	// would allow with no caps, so a correct gate crosses it partway through.
	SwarmCapPercent int `json:"swarm_cap_percent"`
	// TaskCapScale multiplies every task's drawn cap, in percent (100 keeps
	// the corpus draw of $20–60).
	TaskCapScale int `json:"task_cap_scale_percent"`
}

// The registered G3 seed and K (#21): every arm and every N runs the same
// script at each N.
const (
	RegisteredSeed  = 20261021
	RegisteredCalls = 4
)

// Default is the registered G3 script shape for N agents and K calls: tasks
// of 8–15 agents, delegation to depth 3, 20% of depth-3 delegators probing
// one level too deep, the swarm cap at 80% of uncapped allowed spend, and
// task caps as drawn.
func Default(seed uint64, agents, calls int) Config {
	return Config{Seed: seed, Agents: agents, Calls: calls, MaxTaskAgents: 15, ProbePercent: 20,
		SwarmCapPercent: 80, TaskCapScale: 100}
}

// Kinds of call.
const (
	KindWork     = "work"
	KindDelegate = "delegate"
)

// Step is one scripted tool call.
type Step struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Agent int    `json:"agent"`
	// Seq orders an agent's work calls; delegate calls carry their child's
	// position among the agent's delegations.
	Seq int `json:"seq"`
	// Child is the agent a delegate call creates, or -1.
	Child int      `json:"child"`
	Call  gen.Call `json:"call"`
}

// Agent is one scripted agent.
type Agent struct {
	ID    string `json:"id"`
	Role  string `json:"role"`
	Task  int    `json:"task"`
	Depth int    `json:"depth"`
	// Parent is the delegating agent, or -1 for a task's root; Via is the
	// delegate call that creates the agent.
	Parent int    `json:"parent"`
	Via    string `json:"via,omitempty"`
	// Chain is the principal chain the agent should see at run time.
	Chain []corpus.Principal `json:"chain"`
	// Probe marks an agent below the depth limit: the delegation creating it
	// is a forbid under rule 5.2, so it runs only if a gate wrongly allows
	// that delegation. Probes are not counted in Config.Agents.
	Probe       bool   `json:"probe,omitempty"`
	Delegations []Step `json:"delegations"`
	Work        []Step `json:"work"`
}

// Task is one task's tree.
type Task struct {
	ID string `json:"id"`
	gen.SwarmTask
	Root   int   `json:"root"`
	Agents []int `json:"agents"`
}

// Script is a deterministic swarm: every agent, its role, and every call it
// will make, drawn from one seed.
type Script struct {
	Config   Config  `json:"config"`
	Tasks    []Task  `json:"tasks"`
	Agents   []Agent `json:"agents"`
	SwarmCap int64   `json:"swarm_cap_micros"`
	// UncappedAllowed is the spend of every call the oracle allows when no
	// cap binds; the swarm cap is set from it.
	UncappedAllowed int64 `json:"uncapped_allowed_micros"`

	scripted int
}

// childRoles weights a sub-agent's role: pi, analyst, research assistant,
// collaborator. Only the first three may delegate further.
var childRoles = []int{15, 45, 25, 15}

// NewScript draws a script. Each task's tree is grown depth first from its
// root: an agent at depth below MaxDepth whose role may delegate creates
// one to three sub-agents, until the task reaches its drawn size or the
// swarm reaches N agents.
func NewScript(cfg Config) (*Script, error) {
	if cfg.Agents < 1 || cfg.Calls < 0 || cfg.MaxTaskAgents < 1 {
		return nil, fmt.Errorf("swarm: %d agents, %d calls, %d per task: need at least one agent", cfg.Agents, cfg.Calls, cfg.MaxTaskAgents)
	}
	d := gen.NewDrawer(cfg.Seed)
	s := &Script{Config: cfg}
	for ti := 0; s.scripted < cfg.Agents; ti++ {
		st := d.Task(ti)
		st.TaskCap = st.TaskCap * int64(cfg.TaskCapScale) / 100
		t := Task{ID: gen.SwarmTaskID(ti), SwarmTask: st}
		size := min(cfg.MaxTaskAgents/2+d.IntN(cfg.MaxTaskAgents-cfg.MaxTaskAgents/2+1), cfg.Agents-s.scripted)
		size = max(size, 1)
		root := corpus.Principal{ID: st.RootID, Role: st.RootRole, ActingFor: st.User}
		t.Root = s.add(Agent{ID: root.ID, Role: root.Role, Task: ti, Parent: -1, Chain: []corpus.Principal{root}})
		s.scripted++
		t.Agents = []int{t.Root}
		budget := size - 1
		var grow func(a int)
		grow = func(a int) {
			ag := s.Agents[a]
			if !slices.Contains(vocab.Delegators, ag.Role) {
				return
			}
			if ag.Depth == vocab.MaxDepth {
				if d.IntN(100) < cfg.ProbePercent {
					t.Agents = append(t.Agents, s.child(d, &t, a, true))
				}
				return
			}
			n := 1 + d.Weighted([]int{3, 4, 2})
			for k := 0; k < n && budget > 0; k++ {
				budget--
				c := s.child(d, &t, a, false)
				t.Agents = append(t.Agents, c)
				grow(c)
			}
		}
		grow(t.Root)
		s.Tasks = append(s.Tasks, t)
	}
	// Work calls, drawn after the trees so that tree shape does not depend
	// on K.
	for i := range s.Agents {
		a := &s.Agents[i]
		st := s.Tasks[a.Task].SwarmTask
		for k := 0; k < cfg.Calls; k++ {
			a.Work = append(a.Work, Step{ID: fmt.Sprintf("%s.w%d", a.ID, k), Kind: KindWork, Agent: i, Seq: k, Child: -1,
				Call: d.Work(st, a.Role)})
		}
	}
	s.setSwarmCap()
	return s, nil
}

func (s *Script) add(a Agent) int {
	s.Agents = append(s.Agents, a)
	return len(s.Agents) - 1
}

// child adds a sub-agent of parent and the delegate call that creates it.
func (s *Script) child(d *gen.Drawer, t *Task, parent int, probe bool) int {
	p := s.Agents[parent]
	role := vocab.Roles[d.Weighted(childRoles)]
	id := d.AgentID()
	pr := corpus.Principal{ID: id, Role: role, ActingFor: p.ID}
	c := s.add(Agent{ID: id, Role: role, Task: p.Task, Depth: p.Depth + 1, Parent: parent,
		Chain: append(slices.Clone(p.Chain), pr), Probe: probe})
	step := Step{ID: fmt.Sprintf("%s.d%d", p.ID, len(p.Delegations)), Kind: KindDelegate, Agent: parent,
		Seq: len(p.Delegations), Child: c, Call: d.Delegate(t.SwarmTask, id, role)}
	s.Agents[c].Via = step.ID
	if !probe {
		s.scripted++
	}
	s.Agents[parent].Delegations = append(s.Agents[parent].Delegations, step)
	return c
}

// Facts returns the runtime facts of a step for the given spend, as the
// gate will see them.
func (s *Script) Facts(st Step, taskSpent, swarmSpent int64) corpus.Facts {
	a := s.Agents[st.Agent]
	f := st.Call.Facts
	f.Params = cloneParams(f.Params)
	f.Chain = slices.Clone(a.Chain)
	f.Depth = len(a.Chain) - 1
	f.TaskSpent, f.TaskCap = taskSpent, s.Tasks[a.Task].TaskCap
	f.SwarmSpent, f.SwarmCap = swarmSpent, s.SwarmCap
	return f
}

// setSwarmCap sets the cap from the spend the oracle allows with no cap
// binding, over the calls of scripted agents whose creating delegations are
// themselves allowed.
func (s *Script) setSwarmCap() {
	s.SwarmCap = math.MaxInt64 / 4
	var total int64
	var walk func(a int)
	walk = func(a int) {
		ag := s.Agents[a]
		for _, st := range ag.Work {
			if s.allowedUncapped(st) {
				total += st.Call.Facts.EstCost
			}
		}
		for _, st := range ag.Delegations {
			if s.allowedUncapped(st) {
				total += st.Call.Facts.EstCost
				walk(st.Child)
			}
		}
	}
	for _, t := range s.Tasks {
		walk(t.Root)
	}
	s.UncappedAllowed = total
	s.SwarmCap = max(1, total*int64(s.Config.SwarmCapPercent)/100)
}

func (s *Script) allowedUncapped(st Step) bool {
	f := s.Facts(st, 0, 0)
	f.TaskCap = math.MaxInt64 / 4
	return oracle.Verdict(f, st.Call.Semantic) == corpus.Allow
}

// Steps returns every scripted step, in script order.
func (s *Script) Steps() []Step {
	var out []Step
	for _, a := range s.Agents {
		out = append(out, a.Delegations...)
		out = append(out, a.Work...)
	}
	return out
}

// SHA256 is the hash of the script's JSON form; it stands in a swarm run's
// manifest as the corpus hash does in a corpus run's.
func (s *Script) SHA256() (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func cloneParams(p map[string]any) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}
