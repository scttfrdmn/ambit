package swarm

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

// Manifest describes a swarm run. Output, under results/<run>/:
//
//	manifest.json  what ran, on what, when
//	calls.jsonl    one line per gated call: decision, latency, depth, record, oracle truth
//	tasks.jsonl    one line per task: critical path, by depth
//	raw.jsonl      every model reply verbatim
//	quill.jsonl    one LKI quill entry per Cedar decision (B0 and the B arms)
type Manifest struct {
	Run  string `json:"run"`
	Arm  string `json:"arm"`
	Kind string `json:"kind"`
	// CorpusHash is the hash of the corpus whose policies the gate and the
	// oracle read; ScriptSHA256 is the hash of the swarm script, which
	// stands for the frozen corpus (Law 5).
	CorpusHash   string            `json:"corpus_hash"`
	ScriptSHA256 string            `json:"script_sha256"`
	GitCommit    string            `json:"git_commit"`
	GitDirty     bool              `json:"git_dirty"`
	Models       map[string]string `json:"models,omitempty"`
	Started      string            `json:"started"`
	Finished     string            `json:"finished,omitempty"`
	Script       Config            `json:"script"`
	Tasks        int               `json:"tasks"`
	// ScriptedCalls counts the calls of scripted agents; Gated the calls
	// the gate decided, which differ by the subtrees denied delegations
	// cut off and the probes wrongly allowed ones let in.
	ScriptedCalls int   `json:"scripted_calls"`
	Gated         int   `json:"gated"`
	AgentsRan     int   `json:"agents_ran"`
	ProbesRan     int   `json:"probes_ran"`
	SwarmCap      int64 `json:"swarm_cap_micros"`
	SwarmSpent    int64 `json:"swarm_spent_micros"`
	// Concurrency is the in-flight gate cap (0: none). Serial marks a
	// reference run.
	Concurrency int     `json:"concurrency"`
	Serial      bool    `json:"serial,omitempty"`
	WallNS      int64   `json:"wall_ns"`
	Throughput  float64 `json:"calls_per_second"`
	Errors      int     `json:"errors"`
	// RepairableErrors counts transport and capacity errors (run.Repairable).
	// A live swarm cannot be repaired: above 1% of gated calls the run is an
	// instrument failure and is re-run (#21).
	RepairableErrors int `json:"repairable_errors"`
	// QuillEntries counts quill.jsonl's entries, one per Cedar decision.
	QuillEntries int `json:"quill_entries"`
	// SpendMicros is the run's hosted spend from the ledger; ModelCostMicros
	// sums every decision's model cost (hosted API spend, Bedrock at list
	// price). GPU time is priced from InstanceType and the wall time.
	SpendMicros     int64  `json:"spend_micros"`
	RunCapMicros    int64  `json:"run_cap_micros"`
	ModelCostMicros int64  `json:"model_cost_micros"`
	Stopped         string `json:"stopped,omitempty"`
	Host            string `json:"host"`
	// InstanceType is the EC2 instance the swarm, the PDP and the model
	// server shared ("" off EC2: Scott's machine, for the hosted arms).
	InstanceType string `json:"instance_type,omitempty"`
	// ServerSlots is the model server's parallel slots (-server-slots; 0:
	// unrecorded). ScaledServers marks a G3 secondary run, with servers at 4x
	// their G2 slots (-scaled-servers); the report keeps such runs out of the
	// registered verdicts.
	ServerSlots   int    `json:"server_slots,omitempty"`
	ScaledServers bool   `json:"scaled_servers,omitempty"`
	CachePrompt   bool   `json:"cache_prompt"`
	ProseSHA256   string `json:"prose_sha256,omitempty"`
}

// ManifestKind marks a swarm run's manifest.
const ManifestKind = "swarm"

// WriteManifest writes dir/manifest.json.
func WriteManifest(dir string, m Manifest) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0o644)
}

// WriteResults writes calls.jsonl, tasks.jsonl, and raw.jsonl.
func WriteResults(dir string, res Result) error {
	err := writeLines(filepath.Join(dir, "calls.jsonl"), len(res.Calls), func(enc *json.Encoder, i int) error {
		return enc.Encode(res.Calls[i])
	})
	if err != nil {
		return err
	}
	err = writeLines(filepath.Join(dir, "tasks.jsonl"), len(res.Tasks), func(enc *json.Encoder, i int) error {
		return enc.Encode(res.Tasks[i])
	})
	if err != nil {
		return err
	}
	return writeLines(filepath.Join(dir, "raw.jsonl"), len(res.Calls), func(enc *json.Encoder, i int) error {
		for _, raw := range res.Calls[i].Decision.Raw {
			if err := enc.Encode(map[string]any{"id": res.Calls[i].ID, "question": raw.Question, "reply": raw.Reply}); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeLines(path string, n int, line func(*json.Encoder, int) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for i := 0; i < n; i++ {
		if err := line(enc, i); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
