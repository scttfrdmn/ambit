// Vendored from github.com/playgroundlogic/lki (~/src/lki) at commit
// 7687fc4a2f8ee9cdf51cd5b9b7d8dcc35876b69b (2026-05-20). LKI keeps this package
// under internal/, so it cannot be imported; see docs/substrate-brief.md.

package quill

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Writer appends quill entries to a JSONL file.
type Writer struct {
	path string
	f    *os.File
}

// NewWriter opens (or creates) the quill log file at the given path.
// The directory is created if it does not exist.
func NewWriter(path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("quill: mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("quill: open %s: %w", path, err)
	}
	return &Writer{path: path, f: f}, nil
}

// Write appends one quill entry to the log.
// It computes entry_id before writing if not already set.
func (w *Writer) Write(e *Entry) error {
	if e.EntryID == "" {
		id, err := computeEntryID(e)
		if err != nil {
			return fmt.Errorf("quill: compute entry_id: %w", err)
		}
		e.EntryID = id
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("quill: marshal entry: %w", err)
	}
	_, err = fmt.Fprintf(w.f, "%s\n", data)
	return err
}

// Close flushes and closes the quill log.
func (w *Writer) Close() error {
	return w.f.Close()
}

// QuillPath returns the standard quill log path for a cast.
// Convention: .lki/quills/<cast_id>.jsonl relative to the working directory.
func QuillPath(castID string) string {
	return filepath.Join(".lki", "quills", castID+".jsonl")
}

// Now returns the current time as an RFC3339 string with millisecond precision.
func Now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// NowFrom formats a time.Time as RFC3339 with milliseconds.
func NowFrom(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// SummaryLine formats a quill entry as a one-line human-readable summary.
// Format per design/flight-plan-goalpost.md §live-quill-emission:
//
//	cast/8f2a3b4c stoop/03 Filesystem::Write /path/to/file   ALLOW   4ms
//	cast/8f2a3b4c stoop/08 Network::Outbound api.example.com:443/https   THWART   policy: no-network-permitted
func SummaryLine(e *Entry) string {
	// Build the resource/target description
	target := ""
	if len(e.PDPDecisions) > 0 {
		d := e.PDPDecisions[0]
		// Extract action name (strip Lki::Action:: prefix) and resource ID
		action := d.CedarRequest.Action
		if idx := strings.LastIndex(action, "\""); idx > 0 {
			// Has quoted form: Lki::Action::"Filesystem::Write"
			start := strings.LastIndex(action[:idx], "\"")
			if start >= 0 {
				action = action[start+1 : idx]
			}
		}
		resourceID := d.CedarRequest.Resource
		// Strip entity type prefix: Lki::FilePath::"/path" → /path
		if ridx := strings.LastIndex(resourceID, "\""); ridx > 0 {
			rstart := strings.LastIndex(resourceID[:ridx], "\"")
			if rstart >= 0 {
				resourceID = resourceID[rstart+1 : ridx]
			}
		}
		target = action + " " + resourceID
	} else if e.EntryType == "cast_summary" && e.CastSummary != nil {
		return fmt.Sprintf("cast/%s recall  %s", e.CastID, e.CastSummary.RecallReason)
	}

	outcome := strings.ToUpper(e.IntentOutcome)
	if outcome == "DENY" {
		outcome = "THWART"
		// Add determining policy info
		if len(e.PDPDecisions) > 0 && len(e.PDPDecisions[0].DeterminingPolicies) > 0 {
			outcome += "   policy: " + strings.Join(e.PDPDecisions[0].DeterminingPolicies, ",")
		}
	}

	durationStr := "0ms"
	if e.Execution != nil {
		durationStr = fmt.Sprintf("%dms", e.Execution.DurationMS)
	} else if len(e.PDPDecisions) > 0 {
		var totalMS int64
		for _, d := range e.PDPDecisions {
			totalMS += d.EvaluationDurationMS
		}
		durationStr = fmt.Sprintf("%dms", totalMS)
	}

	return fmt.Sprintf("cast/%s stoop/%02d %s   %s   %s",
		e.CastID, e.StoopNumber, target, outcome, durationStr)
}

// computeEntryID computes the SHA-256 content address of an audit entry
// per LKI_FORMAT Chapter 10.2.
//
// Canonicalization:
//  1. Marshal entry to JSON with sorted keys
//  2. SHA-256 the resulting bytes
//  3. Return hex string prefixed with "sha256:"
func computeEntryID(e *Entry) (string, error) {
	// Marshal with sorted keys by using a canonical JSON representation.
	// We JSON-marshal the entry, unmarshal into map[string]any (losing
	// type info), then re-marshal with sorted keys.
	data, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	canonical, err := marshalSorted(raw)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", h), nil
}

// marshalSorted JSON-encodes v with map keys sorted lexicographically.
func marshalSorted(v any) ([]byte, error) {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			kJSON, _ := json.Marshal(k)
			sb.Write(kJSON)
			sb.WriteByte(':')
			vJSON, err := marshalSorted(val[k])
			if err != nil {
				return nil, err
			}
			sb.Write(vJSON)
		}
		sb.WriteByte('}')
		return []byte(sb.String()), nil
	case []any:
		var sb strings.Builder
		sb.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				sb.WriteByte(',')
			}
			itemJSON, err := marshalSorted(item)
			if err != nil {
				return nil, err
			}
			sb.Write(itemJSON)
		}
		sb.WriteByte(']')
		return []byte(sb.String()), nil
	default:
		return json.Marshal(v)
	}
}

// ReadAll reads all quill entries from a JSONL file at path.
func ReadAll(path string) ([]*Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []*Entry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("quill parse: %w", err)
		}
		entries = append(entries, &e)
	}
	return entries, nil
}
