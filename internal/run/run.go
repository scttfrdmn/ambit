// Package run drives one arm over the frozen corpus and writes its results.
//
// A run reads only records.jsonl. It refuses to start unless the corpus
// verifies against SHA256SUMS, and it stamps the corpus hash, git commit,
// and model pins into its manifest (Law 5). Output, under results/<run>/:
//
//	manifest.json    what ran, on what, when
//	decisions.jsonl  one decision per record
//	raw.jsonl        every model reply verbatim
//	quill.jsonl      one LKI quill entry per Cedar decision (B0, B)
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
}

// Options configure a run.
type Options struct {
	CorpusDir  string
	ResultsDir string
	Limit      int
	Models     map[string]string
	// Before is called before each record (to label hosted spend).
	Before func(r corpus.Record)
	// Spent reports the run's hosted spend so far.
	Spent func() int64
	// Progress is called after each record.
	Progress func(i, n int, d arms.Decision)
}

// Run decides every record with g.
func Run(ctx context.Context, g arms.Gate, opt Options) (Manifest, error) {
	hash, err := corpus.Verify(opt.CorpusDir)
	if err != nil {
		return Manifest{}, err
	}
	recs, err := corpus.ReadJSONL[corpus.Record](filepath.Join(opt.CorpusDir, corpus.RecordsFile))
	if err != nil {
		return Manifest{}, err
	}
	if opt.Limit > 0 && opt.Limit < len(recs) {
		recs = recs[:opt.Limit]
	}
	commit, dirty := gitState()
	now := time.Now().UTC()
	m := Manifest{
		Run: fmt.Sprintf("%s-%s", g.Name(), now.Format("20060102T150405Z")), Arm: g.Name(),
		CorpusHash: hash, GitCommit: commit, GitDirty: dirty, Models: opt.Models,
		Started: now.Format(time.RFC3339), Records: len(recs),
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
	decEnc, rawEnc := json.NewEncoder(decF), json.NewEncoder(rawF)

	for i, r := range recs {
		if err := ctx.Err(); err != nil {
			m.Stopped = "interrupted"
			break
		}
		if opt.Before != nil {
			opt.Before(r)
		}
		d := g.Decide(ctx, r)
		if d.Error != "" {
			m.Errors++
			if isSpendStop(d.Error) {
				m.Stopped = d.Error
				break
			}
		}
		if err := decEnc.Encode(d); err != nil {
			return m, err
		}
		for _, raw := range d.Raw {
			if err := rawEnc.Encode(map[string]any{"id": r.ID, "question": raw.Question, "reply": raw.Reply}); err != nil {
				return m, err
			}
		}
		if len(d.Cedar) > 0 {
			if qw == nil {
				if qw, err = quill.NewWriter(filepath.Join(dir, "quill.jsonl")); err != nil {
					return m, err
				}
				defer qw.Close()
			}
			if err := qw.Write(quillEntry(m.Run, i+1, r, d)); err != nil {
				return m, err
			}
		}
		m.Decided++
		if opt.Progress != nil {
			opt.Progress(i+1, len(recs), d)
		}
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

func gitState() (string, bool) {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
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
