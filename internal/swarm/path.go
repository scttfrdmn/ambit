package swarm

import (
	"slices"

	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/vocab"
)

// TaskResult is one task's outcome, with its critical path.
//
// The critical path is defined on the task's dependency graph, with gate
// latency as the only cost (tools are instant): an agent's work calls run
// one after another from the agent's start; its delegate calls all start at
// the agent's start; a sub-agent starts when the delegate call that creates
// it is allowed. The path is the chain of calls with the latest finish:
// the delegations from the root down to some agent, then that agent's work
// calls up to the last. It does not depend on scheduling, so a serial run
// and a concurrent run of the same latencies give the same path; queueing
// for an in-flight slot is excluded and reported as wait.
type TaskResult struct {
	ID       string `json:"id"`
	RootRole string `json:"root_role"`
	// Agents counts the task's scripted agents; Ran the agents that ran,
	// probes included; ProbesRan the probes that ran, each behind a
	// delegation the oracle denies.
	Agents    int `json:"agents"`
	Ran       int `json:"ran"`
	ProbesRan int `json:"probes_ran"`
	// MaxDepth is the depth of the deepest agent that ran.
	MaxDepth int `json:"max_depth"`
	Calls    int `json:"calls"`
	// CriticalNS is the critical path's length; CriticalDepth the depth of
	// the agent that ends it; CriticalCalls its number of calls; ByDepthNS
	// its gate latency by the depth of the calling agent.
	CriticalNS    int64   `json:"critical_ns"`
	CriticalDepth int     `json:"critical_depth"`
	CriticalCalls int     `json:"critical_calls"`
	ByDepthNS     []int64 `json:"critical_by_depth_ns"`
	// WallNS is the measured time from the root's start to its return.
	WallNS      int64 `json:"wall_ns"`
	TaskSpent   int64 `json:"task_spent_micros"`
	TaskCap     int64 `json:"task_cap_micros"`
	FalseAllows int   `json:"false_allows"`
	FalseDenies int   `json:"false_denies"`
}

// CriticalPaths computes each task's result from the recorded calls.
func CriticalPaths(s *Script, calls []Call) []TaskResult {
	byID := make(map[string]*Call, len(calls))
	for i := range calls {
		byID[calls[i].ID] = &calls[i]
	}
	out := make([]TaskResult, len(s.Tasks))
	for ti, t := range s.Tasks {
		r := TaskResult{ID: t.ID, RootRole: t.RootRole, TaskCap: t.TaskCap, CriticalDepth: -1}
		for _, a := range t.Agents {
			if !s.Agents[a].Probe {
				r.Agents++
			}
		}
		var path []int64 // gate latency by depth along the path so far
		var walk func(a int, start int64, prefix []int64, n int)
		walk = func(a int, start int64, prefix []int64, n int) {
			ag := s.Agents[a]
			r.Ran++
			if ag.Probe {
				r.ProbesRan++
			}
			r.MaxDepth = max(r.MaxDepth, ag.Depth)
			at := func(prefix []int64, d int, ns int64) []int64 {
				p := slices.Clone(prefix)
				for len(p) <= d {
					p = append(p, 0)
				}
				p[d] += ns
				return p
			}
			consider := func(finish int64, p []int64, n int) {
				if finish > r.CriticalNS || r.CriticalDepth < 0 {
					r.CriticalNS, r.CriticalDepth, r.CriticalCalls, path = finish, ag.Depth, n, p
				}
			}
			count := func(c *Call) {
				r.Calls++
				if c.Decision.Allow {
					r.TaskSpent += c.Record.Facts.EstCost
				}
				switch {
				case c.Decision.Allow && c.Truth.Verdict == corpus.Deny:
					r.FalseAllows++
				case !c.Decision.Allow && c.Truth.Verdict == corpus.Allow:
					r.FalseDenies++
				}
			}
			for _, st := range ag.Delegations {
				c := byID[st.ID]
				if c == nil {
					continue
				}
				count(c)
				p := at(prefix, ag.Depth, c.GateNS)
				consider(start+c.GateNS, p, n+1)
				if c.Decision.Allow {
					walk(st.Child, start+c.GateNS, p, n+1)
				}
			}
			t, p, k := start, prefix, n
			for _, st := range ag.Work {
				c := byID[st.ID]
				if c == nil {
					continue
				}
				count(c)
				t += c.GateNS
				p = at(p, ag.Depth, c.GateNS)
				k++
				consider(t, p, k)
			}
		}
		walk(t.Root, 0, nil, 0)
		r.ByDepthNS = make([]int64, vocab.MaxDepth+2)
		copy(r.ByDepthNS, path)
		r.CriticalDepth = max(r.CriticalDepth, 0)
		out[ti] = r
	}
	return out
}
