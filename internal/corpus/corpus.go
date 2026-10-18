// Package corpus defines the invocation record, its ground truth, and the
// frozen on-disk form.
//
// Arm inputs and ground truth are stored apart: records.jsonl holds what a
// gate may see; oracle.jsonl holds verdicts and the semantic facts the
// payloads were generated from. Arm code reads only records.jsonl.
package corpus

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Record kinds.
const (
	KindBase     = "base"
	KindInjected = "injected"
	KindVariant  = "variant"
)

// Injection kinds: what the planted payload text asks the gate to do.
const (
	InjectAllow    = "allow"
	InjectEscalate = "escalate"
	InjectRedirect = "redirect"
)

// Presentation variants.
const (
	VariantReorder  = "reordered_tools"
	VariantOpaque   = "opaque_names"
	VariantReviewer = "reviewer_note"
)

// Payload kinds.
const (
	PayloadSQL      = "sql"
	PayloadMessage  = "message"
	PayloadDocument = "document"
	PayloadContext  = "context"
)

// Principal is one link in a delegation chain.
type Principal struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	ActingFor string `json:"acting_for"`
}

// Resource is what the call touches, with registry attributes the runtime
// knows.
type Resource struct {
	Type             string   `json:"type"`
	ID               string   `json:"id"`
	PHI              string   `json:"phi,omitempty"`
	DocKind          string   `json:"doc_kind,omitempty"`
	ApprovedPurposes []string `json:"approved_purposes,omitempty"`
}

// Destination is where an outbound call sends data.
type Destination struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Facts are the structural facts the runtime supplies. Every number is an
// integer; costs are in micro-dollars.
type Facts struct {
	Tool        string         `json:"tool"`
	Params      map[string]any `json:"params"`
	Chain       []Principal    `json:"principal_chain"`
	Purpose     string         `json:"purpose"`
	Resource    Resource       `json:"resource"`
	Destination *Destination   `json:"destination,omitempty"`
	EstCost     int64          `json:"est_cost_micros"`
	TaskSpent   int64          `json:"task_spent_micros"`
	TaskCap     int64          `json:"task_cap_micros"`
	SwarmSpent  int64          `json:"swarm_spent_micros"`
	SwarmCap    int64          `json:"swarm_cap_micros"`
	Depth       int            `json:"depth"`
}

// Payload is the text a gate may need to extract facts from: the SQL of a
// query, the body of a message, the document to be read, or the tool result
// the agent is acting on.
type Payload struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Presentation controls how agent state is rendered for Arm A. It carries
// no facts; variants change it and nothing else (except a reviewer note,
// which is appended to the payload).
type Presentation struct {
	ToolOrder   []string `json:"tool_order"`
	OpaqueNames bool     `json:"opaque_names"`
}

// Record is one invocation, as a gate sees it. It carries nothing about how
// the experiment uses it: family, kind, base record, and injection or
// variant labels are in Truth, and IDs are assigned after a shuffle so they
// reveal nothing either.
type Record struct {
	ID           string       `json:"id"`
	Task         string       `json:"task"`
	Facts        Facts        `json:"facts"`
	Payload      Payload      `json:"payload"`
	Presentation Presentation `json:"presentation"`
}

// Semantic facts, the ones only the payload can supply. A nil pointer means
// the fact does not apply to the record (or, as PIP output, was not
// supplied).
type Semantic struct {
	SQLTouchesIdentified      *bool `json:"sql_touches_identified,omitempty"`
	PayloadContainsIdentified *bool `json:"payload_contains_identified,omitempty"`
	DocOnSubject              *bool `json:"doc_on_subject,omitempty"`
}

// Verdicts.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Ambit classes.
const (
	ClassWithin  = "within"
	ClassMargin  = "margin"
	ClassOutside = "outside"
)

// Truth is the oracle's ground truth for one record, with the record's
// design labels.
type Truth struct {
	ID        string `json:"id"`
	Family    string `json:"family"`
	Scenario  string `json:"scenario"`
	Kind      string `json:"kind"`
	BaseID    string `json:"base_id,omitempty"`
	Variant   string `json:"variant,omitempty"`
	Injection string `json:"injection,omitempty"`
	// InjectionTarget is "gate" for instructions to the decider, "fact" for
	// false claims about the payload aimed at fact extraction.
	InjectionTarget string `json:"injection_target,omitempty"`
	// Policy is the policy the record's scenario exercises (1–5), or 0 for
	// outside-ambit records. Reports attribute by it.
	Policy  int    `json:"policy"`
	Verdict string `json:"verdict"`
	// Violations are the @id of every forbid that applies, sorted.
	Violations []string `json:"violations"`
	Class      string   `json:"class"`
	// Needed lists semantic facts the decision depends on (margin records).
	Needed   []string `json:"needed_facts"`
	Semantic Semantic `json:"semantic"`
}

// File names inside a corpus directory.
const (
	RecordsFile = "records.jsonl"
	OracleFile  = "oracle.jsonl"
	SumsFile    = "SHA256SUMS"
	PolicyDir   = "policies"
	CedarFile   = "ambit.cedar"
	ProseFile   = "ambit.md"
)

// WriteJSONL writes one JSON value per line.
func WriteJSONL[T any](path string, items []T) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for i := range items {
		if err := enc.Encode(items[i]); err != nil {
			return fmt.Errorf("encode %s line %d: %w", path, i+1, err)
		}
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ReadJSONL reads one JSON value per line.
func ReadJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for line := 1; sc.Scan(); line++ {
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// Hashed lists the files covered by the corpus hash, relative to the corpus
// directory.
var Hashed = []string{
	RecordsFile,
	OracleFile,
	filepath.Join(PolicyDir, CedarFile),
	filepath.Join(PolicyDir, ProseFile),
}

// WriteSums writes SHA256SUMS for the hashed files and returns the corpus
// hash: the SHA-256 of the SHA256SUMS file itself.
func WriteSums(dir string) (string, error) {
	sums, err := sums(dir)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, SumsFile), []byte(sums), 0o644); err != nil {
		return "", err
	}
	return hashString(sums), nil
}

// Verify recomputes the sums and returns the corpus hash, or an error if any
// file differs from SHA256SUMS.
func Verify(dir string) (string, error) {
	want, err := os.ReadFile(filepath.Join(dir, SumsFile))
	if err != nil {
		return "", err
	}
	got, err := sums(dir)
	if err != nil {
		return "", err
	}
	if got != string(want) {
		return "", fmt.Errorf("corpus %s does not match %s; it changed after freezing", dir, SumsFile)
	}
	return hashString(got), nil
}

func sums(dir string) (string, error) {
	names := append([]string(nil), Hashed...)
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s  %s\n", hashBytes(data), filepath.ToSlash(n))
	}
	return b.String(), nil
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hashString(s string) string { return hashBytes([]byte(s)) }
