package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/lki/quill"
	"github.com/scttfrdmn/ambit/internal/run"
	"github.com/scttfrdmn/ambit/internal/swarm"
)

// cmdSwarm runs one arm as the gate of a live agenkit swarm (G3) and writes
// results/<arm>-swarm-<N>-<timestamp>/.
func cmdSwarm(args []string) error {
	fs := flag.NewFlagSet("swarm", flag.ExitOnError)
	gf := addGateFlags(fs)
	dir := fs.String("dir", "corpus", "corpus directory whose frozen policies the gate and the oracle read")
	results := fs.String("results", "results", "results directory")
	agents := fs.Int("agents", 10, "N, scripted agents across all tasks")
	calls := fs.Int("calls", swarm.RegisteredCalls, "K, work calls per agent (delegate calls are extra)")
	seed := fs.Uint64("seed", swarm.RegisteredSeed, "script seed (the registered G3 seed)")
	concurrency := fs.Int("concurrency", 0, "gate calls in flight at once (0: no cap)")
	serial := fs.Bool("serial", false, "run one agent at a time (a reference run, not a measurement)")
	slots := fs.Int("server-slots", 0, "the model server's parallel slots, recorded in the manifest (0: unrecorded)")
	scaled := fs.Bool("scaled-servers", false, "a G3 scaled-server secondary run (servers at 4x their G2 slots): reported apart, never in the registered verdicts")
	instance := fs.String("instance-type", "", "EC2 instance type the run shares with its model server, for the GPU cost (empty off EC2)")
	fs.Parse(args)

	// SIGTERM too: a spore TTL stop finalizes what it can, as an interrupt
	// does.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hash, err := corpus.Verify(*dir)
	if err != nil {
		return err
	}
	s, err := swarm.NewScript(swarm.Default(*seed, *agents, *calls))
	if err != nil {
		return err
	}
	scriptHash, err := s.SHA256()
	if err != nil {
		return err
	}
	b, err := gf.build(ctx, *dir, *results)
	if err != nil {
		return err
	}
	commit, dirty := run.GitState()
	host, _ := os.Hostname()
	now := time.Now().UTC()
	mode := ""
	if *serial {
		mode = "-serial"
	}
	m := swarm.Manifest{
		Run: fmt.Sprintf("%s-swarm-%d%s-%s", b.gate.Name(), *agents, mode, now.Format("20060102T150405Z")), Arm: b.gate.Name(),
		Kind: swarm.ManifestKind, CorpusHash: hash, ScriptSHA256: scriptHash, GitCommit: commit, GitDirty: dirty,
		Models: b.models, Started: now.Format(time.RFC3339), Script: s.Config, Tasks: len(s.Tasks),
		SwarmCap: s.SwarmCap, Concurrency: *concurrency, Serial: *serial, Host: host,
		CachePrompt: b.cachePrompt, ProseSHA256: b.proseSHA256, InstanceType: *instance,
		ServerSlots: *slots, ScaledServers: *scaled, RunCapMicros: b.runCapMicros,
	}
	for _, a := range s.Agents {
		if !a.Probe {
			m.ScriptedCalls += len(a.Work) + len(a.Delegations)
		}
	}
	out := filepath.Join(*results, m.Run)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := swarm.WriteManifest(out, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s: %d agents in %d tasks, %d scripted calls, swarm cap $%.2f, script %.12s\n",
		m.Run, *agents, len(s.Tasks), m.ScriptedCalls, float64(s.SwarmCap)/1e6, scriptHash)

	qw, err := quill.NewWriter(filepath.Join(out, "quill.jsonl"))
	if err != nil {
		return err
	}
	defer qw.Close()
	// Calls and raw replies are streamed as the run goes: a run killed
	// before it ends keeps them under its unfinished manifest.
	st, err := swarm.NewStream(out)
	if err != nil {
		return err
	}
	defer st.Close()

	start := time.Now()
	opt := swarm.Options{Concurrency: *concurrency, Serial: *serial, Quill: qw, Run: m.Run, Stream: st,
		// A spend cap or an unbillable account would repeat on every
		// remaining call, as in ambit run; so would an interrupt.
		Stop: func(d arms.Decision) string {
			if ctx.Err() != nil {
				return "interrupted"
			}
			if d.Error != "" && run.IsSpendStop(d.Error) {
				return d.Error
			}
			return ""
		},
		Progress: func(n int, c swarm.Call) {
			if n%max(100, m.ScriptedCalls/50) == 0 {
				fmt.Fprintf(os.Stderr, "%s: %d/%d calls (%s)\n", m.Run, n, m.ScriptedCalls, time.Since(start).Round(time.Second))
			}
			if c.Decision.Error != "" {
				fmt.Fprintf(os.Stderr, "%s: %s: error: %s\n", m.Run, c.ID, c.Decision.Error)
			}
		}}
	res, runErr := swarm.Run(ctx, b.gate, s, opt)
	if runErr != nil && res.Stopped == "" && ctx.Err() != nil {
		res.Stopped = "interrupted"
	}
	if runErr != nil && res.Stopped == "" {
		// The calls made so far are already streamed; the manifest is
		// rewritten stopped, and the report lists the run as incomplete.
		res.Stopped = "harness error: " + runErr.Error()
	}
	m.Gated, m.AgentsRan, m.ProbesRan = len(res.Calls), res.AgentsRan, res.ProbesRan
	m.SwarmSpent, m.WallNS, m.Errors, m.Stopped = res.SwarmSpent, res.WallNS, res.Errors, res.Stopped
	m.RepairableErrors, m.QuillEntries = res.RepairableErrors, res.QuillEntries
	for _, c := range res.Calls {
		m.ModelCostMicros += c.Decision.CostMicros
	}
	if res.WallNS > 0 {
		m.Throughput = float64(len(res.Calls)) / (float64(res.WallNS) / 1e9)
	}
	if b.spent != nil {
		m.SpendMicros = b.spent()
	}
	if err := st.Close(); err != nil && m.Stopped == "" {
		m.Stopped = "harness error: " + err.Error()
	}
	if err := swarm.WriteTasks(out, res.Tasks); err != nil {
		return err
	}
	// Finished marks a complete run only; a stopped one keeps it empty.
	if m.Stopped == "" {
		m.Finished = time.Now().UTC().Format(time.RFC3339)
	}
	if err := swarm.WriteManifest(out, m); err != nil {
		return err
	}
	wrong := 0
	for _, c := range res.Calls {
		if !c.Correct() {
			wrong++
		}
	}
	fmt.Printf("swarm %s: %d calls by %d agents (%d probes) in %s, %.0f calls/s, %d disagree with the oracle, %d errors (%d repairable), %d quill entries, swarm spend $%.2f of $%.2f, hosted spend $%.4f\n",
		m.Run, m.Gated, m.AgentsRan, m.ProbesRan, time.Duration(m.WallNS).Round(time.Millisecond), m.Throughput, wrong, m.Errors,
		m.RepairableErrors, m.QuillEntries, float64(m.SwarmSpent)/1e6, float64(m.SwarmCap)/1e6, float64(m.SpendMicros)/1e6)
	if m.Stopped != "" {
		return fmt.Errorf("swarm stopped: %s", m.Stopped)
	}
	return nil
}
