// Vendored from github.com/playgroundlogic/lki (~/src/lki) at commit
// 7687fc4a2f8ee9cdf51cd5b9b7d8dcc35876b69b (2026-05-20). LKI keeps this package
// under internal/, so it cannot be imported; see docs/substrate-brief.md.

package quill

// Entry is one audit log entry per LKI_FORMAT Chapter 10.
// Every intent invocation produces exactly one Entry, regardless of outcome.
// The EntryID is the SHA-256 of the canonicalized entry body (10.2).
type Entry struct {
	// Identity and timing
	EntryID       string `json:"entry_id"`
	SchemaVersion string `json:"schema_version"` // "0.2"
	Timestamp     string `json:"timestamp"`       // RFC3339 with millisecond precision

	// Cast/stoop context (not in the Chapter 10 schema per se, but required for
	// the gauntlet's append-only quill log — F-048 notes this gap)
	CastID      string `json:"cast_id"`
	StoopNumber int    `json:"stoop_number"`

	// Principal
	Principal PrincipalRecord `json:"principal"`

	// Intent identification
	Intent IntentRecord `json:"intent"`

	// Input
	IntentParameters map[string]any `json:"intent_parameters,omitempty"`
	ResourceBounds   map[string]any `json:"resource_bounds,omitempty"`

	// Observed environment state at request time (10.5)
	ObservedState map[string]map[string]any `json:"observed_state,omitempty"`

	// Capability resolution
	ResolvedCapabilities []ResolvedCapability `json:"resolved_capabilities,omitempty"`

	// PDP evaluation (10.4)
	PDPDecisions []PDPDecisionRecord `json:"pdp_decisions,omitempty"`

	// Atomic outcome
	IntentOutcome string `json:"intent_outcome"` // "Allow" | "Deny" | "Error"

	// Execution (10.6) — only when IntentOutcome is "Allow"
	Execution *ExecutionRecord `json:"execution,omitempty"`

	// EntryType distinguishes normal entries from correction entries (10.3)
	EntryType string `json:"entry_type,omitempty"` // "" | "correction" | "cast_summary"

	// For cast_summary entries
	CastSummary *CastSummaryRecord `json:"cast_summary,omitempty"`
}

// PrincipalRecord records the principal for the stoop.
type PrincipalRecord struct {
	ID        string   `json:"id"`
	SessionID string   `json:"session_id"`
	Labels    []string `json:"labels"`
}

// IntentRecord identifies the intent invoked.
type IntentRecord struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ResolvedCapability is one fully-expanded capability instance.
type ResolvedCapability struct {
	Source   string `json:"source"`   // e.g. "filesystem[0]", "network[0]"
	Purpose  string `json:"purpose"`  // "user_facing" | "implementation_detail"
	Details  string `json:"details"`  // human-readable summary
	ActionName string `json:"action_name"` // Cedar action name e.g. "Filesystem::Write"
	ResourceID string `json:"resource_id"` // entity ID being acted upon
}

// PDPDecisionRecord records one Cedar PDP evaluation per capability (10.4).
type PDPDecisionRecord struct {
	CapabilitySource string        `json:"capability_source"` // e.g. "filesystem[0]"
	CedarRequest     CedarRequest  `json:"cedar_request"`
	Decision         string        `json:"decision"` // "Allow" | "Deny"
	DeterminingPolicies []string   `json:"determining_policies,omitempty"`
	DeterminingReason   string     `json:"determining_reason,omitempty"`
	EvaluationDurationMS int64     `json:"evaluation_duration_ms"`
}

// CedarRequest is the Cedar request shape logged for forensics.
type CedarRequest struct {
	Principal string         `json:"principal"`
	Action    string         `json:"action"`
	Resource  string         `json:"resource"`
	Context   map[string]any `json:"context,omitempty"`
}

// ExecutionRecord records the execution outcome (10.6).
// Present only when IntentOutcome is "Allow".
type ExecutionRecord struct {
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	DurationMS  int64  `json:"duration_ms"`
	Result      string `json:"result"` // "success" | "failure"
	// Failure details
	Error *ExecutionError `json:"error,omitempty"`
	// Compute resources actually consumed
	ActualCost map[string]any `json:"actual_cost,omitempty"`
}

// ExecutionError captures failure details per Chapter 10.6.
type ExecutionError struct {
	Type            string `json:"type"` // "tool_invocation_failure" | "constraint_violation" | "timeout" | "unexpected"
	Message         string `json:"message,omitempty"`
	ExitCode        int    `json:"exit_code,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StructuredReason string `json:"structured_reason,omitempty"`
}

// CastSummaryRecord is emitted as the final quill entry for a cast.
type CastSummaryRecord struct {
	TotalStoops   int    `json:"total_stoops"`
	AllowedStoops int    `json:"allowed_stoops"`
	ThwartedStoops int   `json:"thwarted_stoops"`
	RecallReason  string `json:"recall_reason"`
	DurationMS    int64  `json:"duration_ms"`
}
