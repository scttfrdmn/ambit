// Package run drives one arm over the frozen corpus and writes its results.
//
// A run reads only the records (records.jsonl, or the G2 shards), streamed
// in corpus order. It refuses to start unless the corpus verifies against
// SHA256SUMS, and it stamps the corpus hash, git commit, and model pins into
// its manifest (Law 5). At G2 scale a run covers one range of the corpus, a
// shard, and may decide several records at once. Output, under
// results/<run>/:
//
//	manifest.json    what ran, on what, when
//	decisions.jsonl  one decision per record
//	raw.jsonl        every model reply verbatim
//	quill.jsonl      one LKI quill entry per Cedar decision (B0, B)
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/lki/quill"
)

// Manifest describes a run.
type Manifest struct {
	Run        string            `json:"run"`
	Arm        string            `json:"arm"`
	CorpusHash string            `json:"corpus_hash"`
	GitCommit  string            `json:"git_commit"`
	GitDirty   bool              `json:"git_dirty"`
	Models     map[string]string `json:"models,omitempty"`
	Started    string            `json:"started"`
	Finished   string            `json:"finished,omitempty"`
	Records    int               `json:"records"`
	Decided    int               `json:"decided"`
	Errors     int               `json:"errors"`
	// SpendMicros is the run's hosted spend.
	SpendMicros int64  `json:"spend_micros"`
	Stopped     string `json:"stopped,omitempty"`
	Notes       string `json:"notes,omitempty"`
	// RangeStart and RangeEnd bound the records decided, [start, end) in
	// corpus order; Records is their count. A manifest without them (G1)
	// covers the whole corpus.
	RangeStart  int    `json:"range_start"`
	RangeEnd    int    `json:"range_end"`
	Concurrency int    `json:"concurrency"`
	Host        string `json:"host"`
	// CachePrompt records whether llama-server reused prompt prefixes.
	CachePrompt bool `json:"cache_prompt"`
	// ProseSHA256 is the hash of the policy prose an Arm A model read, and
	// Ablation names the registered prose ablation, if any, that produced it.
	ProseSHA256 string `json:"prose_sha256,omitempty"`
	Ablation    string `json:"ablation,omitempty"`
	// RepairOf names the runs whose errored decisions this run re-asks. A
	// repair run decides only those records; the report substitutes its
	// decisions for the errors and for nothing else.
	RepairOf []string `json:"repair_of,omitempty"`
}

// Options configure a run.
type Options struct {
	CorpusDir  string
	ResultsDir string
	// Offset and Limit select records [Offset, Offset+Limit) in corpus
	// order; Limit 0 runs to the end.
	Offset int
	Limit  int
	// Concurrency is the number of records decided at once; 0 or 1 is
	// sequential (G1). Above 1, output lines are written as decisions
	// complete, so their order varies; each carries its record ID.
	Concurrency int
	Models      map[string]string
	CachePrompt bool
	ProseSHA256 string
	Ablation    string
	// IDs, if set, restricts the run to these records (a repair run), and
	// RepairOf names the runs they come from.
	IDs      map[string]bool
	RepairOf []string
	// Spent reports the run's hosted spend so far.
	Spent func() int64
	// Progress is called after each record, with the count decided so far.
	Progress func(i, n int, d arms.Decision)
}

// errDone ends a corpus scan early.
var errDone = errors.New("done")

// span resolves Offset and Limit against the corpus, reading no further
// than it must.
func span(dir string, offset, limit int) (int, int, error) {
	if offset < 0 || limit < 0 {
		return 0, 0, fmt.Errorf("offset %d, limit %d: must not be negative", offset, limit)
	}
	n := 0
	err := corpus.Each(dir, "records", func(json.RawMessage) error {
		n++
		if limit > 0 && n >= offset+limit {
			return errDone
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDone) {
		return 0, 0, err
	}
	if offset > 0 && offset >= n {
		return 0, 0, fmt.Errorf("offset %d is past the corpus's %d records", offset, n)
	}
	return offset, n, nil
}

// job is one record and its index in the corpus.
type job struct {
	i int
	r corpus.Record
}

// Run decides the records in range with g.
func Run(ctx context.Context, g arms.Gate, opt Options) (Manifest, error) {
	hash, err := corpus.Verify(opt.CorpusDir)
	if err != nil {
		return Manifest{}, err
	}
	start, end, err := span(opt.CorpusDir, opt.Offset, opt.Limit)
	if err != nil {
		return Manifest{}, err
	}
	workers := max(opt.Concurrency, 1)
	commit, dirty := gitState()
	host, _ := os.Hostname()
	now := time.Now().UTC()
	m := Manifest{
		Run: fmt.Sprintf("%s-%s", g.Name(), now.Format("20060102T150405Z")), Arm: g.Name(),
		CorpusHash: hash, GitCommit: commit, GitDirty: dirty, Models: opt.Models,
		Started: now.Format(time.RFC3339), Records: end - start,
		RangeStart: start, RangeEnd: end, Concurrency: workers, Host: host,
		CachePrompt: opt.CachePrompt, ProseSHA256: opt.ProseSHA256, Ablation: opt.Ablation,
	}
	if opt.Offset > 0 || opt.Limit > 0 {
		// A shard's name carries its range, so shards started together do
		// not collide.
		m.Run += fmt.Sprintf("-%d-%d", start, end)
	}
	if opt.IDs != nil {
		m.Run += "-repair"
		m.Records, m.RepairOf = len(opt.IDs), opt.RepairOf
	}
	dir := filepath.Join(opt.ResultsDir, m.Run)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return m, err
	}
	writeManifest := func() error {
		b, _ := json.MarshalIndent(m, "", "  ")
		return os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0o644)
	}
	if err := writeManifest(); err != nil {
		return m, err
	}
	decF, err := os.Create(filepath.Join(dir, "decisions.jsonl"))
	if err != nil {
		return m, err
	}
	defer decF.Close()
	rawF, err := os.Create(filepath.Join(dir, "raw.jsonl"))
	if err != nil {
		return m, err
	}
	defer rawF.Close()
	var qw *quill.Writer
	defer func() {
		if qw != nil {
			qw.Close()
		}
	}()
	decEnc, rawEnc := json.NewEncoder(decF), json.NewEncoder(rawF)

	// mu guards m, the output files, and halt. Once halt is set (a spend
	// stop, an interrupt, or a write error) no new record is started;
	// decisions already in flight finish and are written.
	var mu sync.Mutex
	var halt bool
	var werr error
	stop := func(reason string) {
		halt = true
		if m.Stopped == "" {
			m.Stopped = reason
		}
	}
	write := func(j job, d arms.Decision) error {
		if err := decEnc.Encode(d); err != nil {
			return err
		}
		for _, raw := range d.Raw {
			if err := rawEnc.Encode(map[string]any{"id": j.r.ID, "question": raw.Question, "reply": raw.Reply}); err != nil {
				return err
			}
		}
		if len(d.Cedar) > 0 {
			if qw == nil {
				var err error
				if qw, err = quill.NewWriter(filepath.Join(dir, "quill.jsonl")); err != nil {
					return err
				}
			}
			if err := qw.Write(quillEntry(m.Run, j.i+1, j.r, d)); err != nil {
				return err
			}
		}
		return nil
	}
	// halted reports whether to skip a record, marking an interrupt.
	halted := func() bool {
		mu.Lock()
		defer mu.Unlock()
		if !halt && ctx.Err() != nil {
			stop("interrupted")
		}
		return halt
	}

	jobs := make(chan job)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if halted() {
					continue
				}
				d := g.Decide(arms.WithRecord(ctx, j.r.ID), j.r)
				mu.Lock()
				if d.Error != "" {
					m.Errors++
					if isSpendStop(d.Error) {
						stop(d.Error)
						mu.Unlock()
						continue
					}
				}
				if werr == nil {
					if err := write(j, d); err != nil {
						werr, halt = err, true
					} else {
						m.Decided++
						if opt.Progress != nil {
							opt.Progress(m.Decided, m.Records, d)
						}
					}
				}
				mu.Unlock()
			}
		}()
	}
	i := 0
	err = corpus.Each(opt.CorpusDir, "records", func(r corpus.Record) error {
		defer func() { i++ }()
		if i < start {
			return nil
		}
		if i >= end || halted() {
			return errDone
		}
		if opt.IDs != nil && !opt.IDs[r.ID] {
			return nil
		}
		jobs <- job{i, r}
		return nil
	})
	close(jobs)
	wg.Wait()
	if err != nil && !errors.Is(err, errDone) {
		return m, err
	}
	if werr != nil {
		return m, werr
	}
	if opt.Spent != nil {
		m.SpendMicros = opt.Spent()
	}
	m.Finished = time.Now().UTC().Format(time.RFC3339)
	return m, writeManifest()
}

// isSpendStop reports errors that would repeat on every remaining record:
// a spend cap, or a hosted account that cannot be billed or is not
// authorized. Recording them as decisions would score outages as denials.
func isSpendStop(e string) bool {
	for _, s := range []string{"per-run cap", "project ceiling", "insufficient_quota", "credit_balance_exhausted", "401 Unauthorized", "403 Forbidden"} {
		if strings.Contains(e, s) {
			return true
		}
	}
	return false
}

// gitState returns the commit and whether tracked files differ from it. On
// a spore.host instance there is no repository; scripts/spore/launch.sh
// refuses a dirty tree and ships the commit in a GIT_COMMIT file instead.
func gitState() (string, bool) {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		if b, ferr := os.ReadFile("GIT_COMMIT"); ferr == nil {
			return strings.TrimSpace(string(b)), false
		}
		return "unknown", true
	}
	st, _ := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output()
	return strings.TrimSpace(string(out)), len(strings.TrimSpace(string(st))) > 0
}

// quillEntry records a Cedar decision in LKI's audit format. PIP-supplied
// facts go in observed_state["pip"], with their probabilities.
func quillEntry(run string, n int, r corpus.Record, d arms.Decision) *quill.Entry {
	f := r.Facts
	actor := f.Chain[len(f.Chain)-1]
	chain := make([]string, len(f.Chain))
	for i, p := range f.Chain {
		chain[i] = p.Role
	}
	e := &quill.Entry{
		SchemaVersion:    "0.2",
		Timestamp:        quill.Now(),
		CastID:           run,
		StoopNumber:      n,
		Principal:        quill.PrincipalRecord{ID: actor.ID, SessionID: f.Chain[0].ID, Labels: chain},
		Intent:           quill.IntentRecord{Name: f.Tool, Version: "ambit-g0"},
		IntentParameters: f.Params,
		ObservedState: map[string]map[string]any{
			"record": {"id": r.ID},
		},
		IntentOutcome: "Deny",
	}
	if d.Allow {
		e.IntentOutcome = "Allow"
	}
	if d.Error != "" {
		e.IntentOutcome = "Error"
		e.ObservedState["error"] = map[string]any{"message": d.Error}
	}
	if len(d.Facts) > 0 {
		pip := map[string]any{}
		for k, v := range d.Facts {
			pip[k] = map[string]any{"value": v.Value, "p": v.P}
		}
		e.ObservedState["pip"] = pip
	}
	for i, c := range d.Cedar {
		rec := quill.PDPDecisionRecord{
			CapabilitySource: fmt.Sprintf("evaluation[%d]", i),
			CedarRequest: quill.CedarRequest{
				Principal: c.Query.Principal.Type + "::\"" + c.Query.Principal.ID + "\"",
				Action:    c.Query.Action,
				Resource:  c.Query.Resource.Type + "::\"" + c.Query.Resource.ID + "\"",
				Context:   c.Query.Context,
			},
			Decision:             "Deny",
			DeterminingPolicies:  c.Decision.DeterminingPolicies,
			EvaluationDurationMS: c.Duration.Milliseconds(),
		}
		if c.Decision.Allowed {
			rec.Decision = "Allow"
		}
		switch {
		case len(c.Decision.DeterminingPolicies) > 0:
			rec.DeterminingReason = strings.Join(c.Decision.DeterminingPolicies, ", ")
		case !c.Decision.Allowed:
			rec.DeterminingReason = "no policy permits this action: outside ambit, default deny"
		}
		e.PDPDecisions = append(e.PDPDecisions, rec)
		e.ObservedState[fmt.Sprintf("timing_%d", i)] = map[string]any{"evaluation_ns": c.Duration.Nanoseconds()}
	}
	return e
}

// Repairable reports whether a gate error is a transport or capacity failure,
// one where the model never evaluated the request: rate-limit 429s (not quota
// or credit), 5xx, timeouts and broken connections. Only these are re-asked
// by a repair run; any other gate error stays a denial (registration #33).
func Repairable(e string) bool {
	if e == "" || strings.Contains(e, "insufficient_quota") || strings.Contains(e, "credit_balance_exhausted") {
		return false
	}
	for _, s := range []string{"429 Too Many Requests", "500 Internal Server Error", "502 Bad Gateway", "503 Service Unavailable",
		"504 Gateway Timeout", "context deadline exceeded", "Client.Timeout", "connection reset", "connection refused", "unexpected EOF", "i/o timeout"} {
		if strings.Contains(e, s) {
			return true
		}
	}
	return false
}
