// Package swarm runs a live agenkit swarm with every tool call gated (G3,
// #21, H-scale).
//
// A script (script.go) fixes, from one seed, N agents organized as one tree
// per task: a root coordinator delegates subtasks to sub-agents down to
// depth 3, and every agent makes K work calls drawn with the corpus's
// scenario builders. At run time:
//
//   - Each agent is an agenkit.Agent. A delegation is an agenkit
//     patterns.AgentTool wrapping the sub-agent, so a sub-agent runs inside
//     its parent's delegate call.
//   - agenkit has no principal and AgentTool drops message metadata, so the
//     principal chain travels in context.Context. The delegate tool appends
//     the sub-agent's principal before it runs the sub-agent.
//   - The gate (PEP) is an agenkit.Tool decorator, Gated, around every
//     tool, delegate tools included. It builds the corpus.Record from the
//     runtime facts (the chain from the context, live task and swarm spend,
//     the caps), asks the arms.Gate, records the decision with the oracle's
//     verdict on the same facts, then runs the tool or returns a denial.
//   - Spend is live: an allowed call adds its estimated cost to atomic task
//     and swarm counters, so a later call can cross a cap mid-run. The spend
//     a gate sees is read when the call is made and added when it is
//     allowed; concurrent calls can therefore each see headroom that only one
//     of them can use. That is how a live PEP without reservations behaves,
//     and each record is judged on the facts its gate saw.
//   - All agents run at once: an agent starts its delegations together,
//     each in its own goroutine, and makes its work calls one at a time
//     meanwhile. Concurrency caps the gate calls in flight.
//
// Tools are fakes that return at once, so the only latency on a task's
// critical path is the gates' (path.go).
package swarm

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scttfrdmn/agenkit-go/agenkit"
	"github.com/scttfrdmn/agenkit-go/patterns"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/gen"
	"github.com/scttfrdmn/ambit/internal/lki/quill"
	"github.com/scttfrdmn/ambit/internal/oracle"
	"github.com/scttfrdmn/ambit/internal/run"
	"github.com/scttfrdmn/ambit/internal/vocab"
)

// --- The principal chain, in context ---

type chainKey struct{}

// WithPrincipal returns ctx with p appended to its principal chain. The
// chain is copied, so sibling delegations never share a backing array.
func WithPrincipal(ctx context.Context, p corpus.Principal) context.Context {
	c := ChainOf(ctx)
	next := make([]corpus.Principal, len(c), len(c)+1)
	copy(next, c)
	return context.WithValue(ctx, chainKey{}, append(next, p))
}

// ChainOf returns the principal chain carried by ctx, root first. The
// caller must not modify it.
func ChainOf(ctx context.Context) []corpus.Principal {
	c, _ := ctx.Value(chainKey{}).([]corpus.Principal)
	return c
}

// stepKey carries the scripted step an agent is making: its call ID and the
// payload the runtime holds for it. taskKey carries the task's live state.
type stepKey struct{}
type taskKey struct{}

func withStep(ctx context.Context, s *Step) context.Context {
	return context.WithValue(ctx, stepKey{}, s)
}

func stepOf(ctx context.Context) *Step {
	s, _ := ctx.Value(stepKey{}).(*Step)
	return s
}

// taskState is one task's live state.
type taskState struct {
	t     *Task
	spent atomic.Int64
}

func taskOf(ctx context.Context) *taskState {
	t, _ := ctx.Value(taskKey{}).(*taskState)
	return t
}

// --- Records ---

// Call is one gated call, as the harness records it.
type Call struct {
	ID    string `json:"id"`
	Task  string `json:"task"`
	Agent string `json:"agent"`
	Kind  string `json:"kind"`
	Depth int    `json:"depth"`
	Seq   int    `json:"seq"`
	// Via is the delegate call that created the calling agent ("" for a
	// root), and Child the agent a delegate call creates.
	Via   string `json:"via,omitempty"`
	Child string `json:"child,omitempty"`
	Probe bool   `json:"probe,omitempty"`
	// StartNS is when the gate call began, from the swarm's start; WaitNS is
	// time spent waiting for an in-flight slot; GateNS is the gate's wall
	// time as the agent sees it, excluding the wait.
	StartNS  int64           `json:"start_ns"`
	WaitNS   int64           `json:"wait_ns"`
	GateNS   int64           `json:"gate_ns"`
	Decision arms.Decision   `json:"decision"`
	Record   corpus.Record   `json:"record"`
	Truth    corpus.Truth    `json:"truth"`
	Scenario string          `json:"scenario"`
	Semantic corpus.Semantic `json:"-"`
}

// Correct reports whether the gate agreed with the oracle.
func (c Call) Correct() bool { return c.Decision.Allow == (c.Truth.Verdict == corpus.Allow) }

// Options configure a run.
type Options struct {
	// Concurrency caps the gate calls in flight; 0 is no cap.
	Concurrency int
	// Serial runs one agent at a time in script order, delegations before
	// work, so that the spend every gate sees is reproducible. For tests
	// and reference runs; a measured run is concurrent.
	Serial bool
	// Stop, if set, is consulted with each decision; a non-empty reason
	// halts the swarm (a hosted spend cap). Calls after a halt are denied
	// without asking the gate and are not recorded.
	Stop func(arms.Decision) string
	// Progress is called after each gated call with the count so far.
	Progress func(n int, c Call)
	// Quill, if set, receives one LKI quill entry per decision that carries
	// Cedar evaluations (the B arms and B0), as an ambit run's quill.jsonl
	// does; Run names the cast. Entries are written off the agents' path,
	// in the order the decisions are recorded.
	Quill *quill.Writer
	Run   string
}

// Result is a finished run.
type Result struct {
	Calls      []Call       `json:"-"`
	Tasks      []TaskResult `json:"-"`
	WallNS     int64        `json:"wall_ns"`
	SwarmSpent int64        `json:"swarm_spent_micros"`
	AgentsRan  int          `json:"agents_ran"`
	ProbesRan  int          `json:"probes_ran"`
	Errors     int          `json:"errors"`
	// RepairableErrors counts the errors run.Repairable accepts: transport
	// and capacity failures. A live swarm cannot be repaired after the
	// fact; above 1% of gate calls the run is an instrument failure (#21).
	RepairableErrors int `json:"repairable_errors"`
	// QuillEntries counts the quill entries written.
	QuillEntries int    `json:"quill_entries"`
	Stopped      string `json:"stopped,omitempty"`
}

// --- The harness ---

type harness struct {
	s     *Script
	g     arms.Gate
	opt   Options
	start time.Time
	slots chan struct{}
	swarm atomic.Int64
	ran   []atomic.Bool
	wall  []atomic.Int64 // per task, root start to return
	tools map[string]agenkit.Tool

	mu         sync.Mutex
	calls      []Call
	errors     int
	repairable int
	stopped    string

	// quill hands decisions with Cedar evaluations to the quill writer.
	quill    chan quillJob
	quillN   int
	quillErr error
}

// quillJob is one decision for the quill, with its stoop number.
type quillJob struct {
	n int
	c Call
}

// Run runs the script with every tool call gated by g.
func Run(ctx context.Context, g arms.Gate, s *Script, opt Options) (Result, error) {
	h := &harness{s: s, g: g, opt: opt, ran: make([]atomic.Bool, len(s.Agents)),
		wall: make([]atomic.Int64, len(s.Tasks)), tools: map[string]agenkit.Tool{}}
	if opt.Concurrency > 0 {
		h.slots = make(chan struct{}, opt.Concurrency)
	}
	// A work call can name delegate too: a collaborator's delegation, which
	// policy 4 forbids. It has no scripted sub-agent, so if a gate allows it
	// the fake tool runs and nothing is created.
	for _, t := range vocab.Tools {
		h.tools[t.Name] = &Gated{Inner: &fakeTool{name: t.Name, desc: t.Description}, h: h}
	}
	for _, name := range vocab.UnknownTools {
		h.tools[name] = &Gated{Inner: &fakeTool{name: name, desc: vocab.UnknownToolDescriptions[name]}, h: h}
	}
	var qdone chan struct{}
	if opt.Quill != nil {
		h.quill, qdone = make(chan quillJob, 4096), make(chan struct{})
		go func() {
			defer close(qdone)
			for j := range h.quill {
				if h.quillErr != nil {
					continue
				}
				e := run.QuillEntry(opt.Run, j.n, j.c.Record, j.c.Decision)
				e.ObservedState["swarm"] = map[string]any{"task": j.c.Task, "agent": j.c.Agent, "kind": j.c.Kind,
					"depth": j.c.Depth, "gate_ns": j.c.GateNS}
				h.quillErr = opt.Quill.Write(e)
			}
		}()
	}
	h.start = time.Now()
	var wg sync.WaitGroup
	errs := make([]error, len(s.Tasks))
	for i := range s.Tasks {
		ts := &taskState{t: &s.Tasks[i]}
		root := s.Agents[ts.t.Root]
		rctx := context.WithValue(WithPrincipal(ctx, root.Chain[0]), taskKey{}, ts)
		runRoot := func() {
			t0 := time.Now()
			_, errs[i] = h.agent(ts.t.Root).Process(rctx, agenkit.NewMessage("user", ts.t.Text))
			h.wall[i].Store(time.Since(t0).Nanoseconds())
		}
		if opt.Serial {
			runRoot()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			runRoot()
		}()
	}
	wg.Wait()
	res := Result{WallNS: time.Since(h.start).Nanoseconds(), SwarmSpent: h.swarm.Load(),
		Calls: h.calls, Errors: h.errors, RepairableErrors: h.repairable, QuillEntries: h.quillN, Stopped: h.stopped}
	if h.quill != nil {
		close(h.quill)
		<-qdone
		if h.quillErr != nil {
			return res, fmt.Errorf("quill: %w", h.quillErr)
		}
	}
	for i := range h.ran {
		if h.ran[i].Load() {
			res.AgentsRan++
			if s.Agents[i].Probe {
				res.ProbesRan++
			}
		}
	}
	for _, err := range errs {
		if err != nil {
			return res, err
		}
	}
	slices.SortFunc(res.Calls, func(a, b Call) int {
		return compareIDs(a.ID, b.ID)
	})
	res.Tasks = CriticalPaths(s, res.Calls)
	for i := range res.Tasks {
		res.Tasks[i].WallNS = h.wall[i].Load()
	}
	return res, nil
}

func compareIDs(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (h *harness) record(c Call) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.quill != nil && len(c.Decision.Cedar) > 0 {
		h.quillN++
		h.quill <- quillJob{h.quillN, c}
	}
	// The Cedar evaluations go to the quill only; the run keeps the rest.
	c.Decision.Cedar = nil
	h.calls = append(h.calls, c)
	if c.Decision.Error != "" {
		h.errors++
		if run.Repairable(c.Decision.Error) {
			h.repairable++
		}
	}
	if h.stopped == "" && h.opt.Stop != nil {
		h.stopped = h.opt.Stop(c.Decision)
	}
	if h.opt.Progress != nil {
		h.opt.Progress(len(h.calls), c)
	}
}

func (h *harness) halted() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopped != ""
}

// --- Agents and tools ---

// scriptAgent is one scripted agent.
type scriptAgent struct {
	h *harness
	i int
}

func (h *harness) agent(i int) *scriptAgent { return &scriptAgent{h: h, i: i} }

// Name returns the agent's ID.
func (a *scriptAgent) Name() string { return a.h.s.Agents[a.i].ID }

// Capabilities lists the agent's role.
func (a *scriptAgent) Capabilities() []string { return []string{"role:" + a.h.s.Agents[a.i].Role} }

// Introspect reports the agent's place in its task.
func (a *scriptAgent) Introspect() *agenkit.IntrospectionResult {
	ag := a.h.s.Agents[a.i]
	r, _ := agenkit.NewIntrospectionResult(ag.ID, a.Capabilities(), nil,
		map[string]any{"depth": ag.Depth, "delegations": len(ag.Delegations), "work": len(ag.Work)}, nil)
	return r
}

// Process runs the agent's script: it starts its delegations, each in its
// own goroutine, and makes its work calls in order meanwhile. It refuses to
// run under a chain that does not end with itself.
func (a *scriptAgent) Process(ctx context.Context, msg *agenkit.Message) (*agenkit.Message, error) {
	ag := &a.h.s.Agents[a.i]
	c := ChainOf(ctx)
	if len(c) == 0 || c[len(c)-1].ID != ag.ID {
		return nil, fmt.Errorf("agent %s run under a chain ending %v", ag.ID, c)
	}
	a.h.ran[a.i].Store(true)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	do := func(st *Step, t agenkit.Tool) {
		res, err := t.Execute(withStep(ctx, st), cloneParams(st.Call.Facts.Params))
		if err == nil && res != nil && !res.Success && res.Metadata["denied"] == nil {
			err = fmt.Errorf("%s: %s", st.ID, res.Error)
		}
		if err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}
	}
	for k := range ag.Delegations {
		st := &ag.Delegations[k]
		t, err := a.delegateTool(st)
		if err != nil {
			return nil, err
		}
		if a.h.opt.Serial {
			do(st, t)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			do(st, t)
		}()
	}
	for k := range ag.Work {
		st := &ag.Work[k]
		do(st, a.h.tools[st.Call.Facts.Tool])
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return agenkit.NewMessage("agent", fmt.Sprintf("%s done: %s", ag.ID, msg.ContentString())), nil
}

// delegateTool is the gated tool for one delegation: the sub-agent wrapped
// as an agenkit AgentTool, behind the chain-appending delegate tool.
func (a *scriptAgent) delegateTool(st *Step) (agenkit.Tool, error) {
	at, err := patterns.NewAgentTool(patterns.AgentToolConfig{
		Agent: a.h.agent(st.Child), Name: vocab.ToolDelegate,
		Description: "Spawn a sub-agent with a given role to carry out a subtask.", InputKey: "subtask",
	})
	if err != nil {
		return nil, err
	}
	return &Gated{Inner: &delegateTool{inner: at}, h: a.h}, nil
}

// delegateTool appends the sub-agent's principal, named by the call's
// parameters, to the chain, then runs the sub-agent.
type delegateTool struct {
	inner *patterns.AgentTool
}

func (d *delegateTool) Name() string        { return d.inner.Name() }
func (d *delegateTool) Description() string { return d.inner.Description() }

// Execute runs the sub-agent under the extended chain.
func (d *delegateTool) Execute(ctx context.Context, params map[string]any) (*agenkit.ToolResult, error) {
	c := ChainOf(ctx)
	id, _ := params["agent_id"].(string)
	role, _ := params["role"].(string)
	if len(c) == 0 || id == "" || role == "" {
		return agenkit.NewToolError("delegate: no caller or no sub-agent"), nil
	}
	res, err := d.inner.Execute(WithPrincipal(ctx, corpus.Principal{ID: id, Role: role, ActingFor: c[len(c)-1].ID}), params)
	if err == nil && res != nil && !res.Success {
		err = fmt.Errorf("delegate to %s: %s", id, res.Error)
	}
	return res, err
}

// fakeTool returns at once.
type fakeTool struct {
	name, desc string
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return f.desc }

// Execute returns an empty success.
func (f *fakeTool) Execute(context.Context, map[string]any) (*agenkit.ToolResult, error) {
	return agenkit.NewToolResult("ok"), nil
}

// Gated is the policy enforcement point: an agenkit.Tool decorator that
// asks the arm's gate before the inner tool runs.
type Gated struct {
	Inner agenkit.Tool
	h     *harness
}

// Name returns the inner tool's name.
func (t *Gated) Name() string { return t.Inner.Name() }

// Description returns the inner tool's description.
func (t *Gated) Description() string { return t.Inner.Description() }

// Execute builds the invocation record from the runtime facts, asks the
// gate, records the decision and the oracle's verdict on the same facts,
// then runs the inner tool or returns a denial. A denial is a tool result,
// not an error: the agent carries on.
func (t *Gated) Execute(ctx context.Context, params map[string]any) (*agenkit.ToolResult, error) {
	h := t.h
	st, ts := stepOf(ctx), taskOf(ctx)
	chain := ChainOf(ctx)
	if st == nil || ts == nil || len(chain) == 0 {
		return nil, fmt.Errorf("gate %s: call without a step, task, or principal", t.Name())
	}
	if h.halted() {
		return agenkit.NewToolError("swarm halted").WithMetadata("denied", true), nil
	}
	ag := h.s.Agents[st.Agent]
	f := st.Call.Facts
	f.Params = params
	f.Chain = slices.Clone(chain)
	f.Depth = len(chain) - 1
	f.TaskCap, f.SwarmCap = ts.t.TaskCap, h.s.SwarmCap
	rec := corpus.Record{ID: st.ID, Task: ts.t.Text, Payload: st.Call.Payload,
		Presentation: corpus.Presentation{ToolOrder: gen.ToolNames()}}

	t0 := time.Now()
	if h.slots != nil {
		select {
		case h.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	t1 := time.Now()
	// Spend is read once the call has a slot: the facts are as of the
	// moment the gate is asked.
	f.TaskSpent, f.SwarmSpent = ts.spent.Load(), h.swarm.Load()
	rec.Facts = f
	d := h.g.Decide(arms.WithRecord(ctx, rec.ID), rec)
	gateNS := time.Since(t1).Nanoseconds()
	if h.slots != nil {
		<-h.slots
	}
	if d.Allow {
		ts.spent.Add(f.EstCost)
		h.swarm.Add(f.EstCost)
	}
	truth := oracle.Judge(rec, st.Call.Semantic, st.Call.Policy)
	truth.Family, truth.Scenario, truth.Kind = ts.t.ID, st.Call.Scenario, KindSwarm
	c := Call{ID: st.ID, Task: ts.t.ID, Agent: ag.ID, Kind: st.Kind, Depth: f.Depth, Seq: st.Seq,
		Via: ag.Via, Probe: ag.Probe, StartNS: t0.Sub(h.start).Nanoseconds(), WaitNS: t1.Sub(t0).Nanoseconds(),
		GateNS: gateNS, Decision: d, Record: rec, Truth: truth, Scenario: st.Call.Scenario, Semantic: st.Call.Semantic}
	if st.Child >= 0 {
		c.Child = h.s.Agents[st.Child].ID
	}
	h.record(c)
	if !d.Allow {
		reason := "denied by gate"
		if len(d.Determining) > 0 {
			reason += ": " + fmt.Sprint(d.Determining)
		}
		return agenkit.NewToolError(reason).WithMetadata("denied", true), nil
	}
	return t.Inner.Execute(ctx, params)
}

// KindSwarm is the Truth.Kind of a live-swarm call.
const KindSwarm = "swarm"
