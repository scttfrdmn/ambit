// Vendored from github.com/playgroundlogic/lki (~/src/lki) at commit
// 7687fc4a2f8ee9cdf51cd5b9b7d8dcc35876b69b (2026-05-20). LKI keeps this package
// under internal/, so it cannot be imported; see docs/substrate-brief.md.
//
// Local change: LoadPolicies keys each policy by its @id annotation (falling
// back to policyN), so determining policies carry rule names.

package pdp

import (
	"encoding/json"
	"fmt"

	cedar "github.com/cedar-policy/cedar-go"
)

// Schema is a parsed Cedar schema. Opaque wrapper around cedar-go internals.
type Schema struct {
	// raw holds the schema text for informational purposes;
	// actual validation uses cedar-go's schema type.
	raw string
	// Note: cedar-go's experimental schema package doesn't yet validate
	// policy requests; we hold the schema for future use when the validator
	// stabilizes. The current authorizer works without schema validation.
}

// Policies is a parsed Cedar policy set, ready for authorization.
type Policies struct {
	ps *cedar.PolicySet
}

// Entity is a Cedar entity (principal or resource) with typed attributes.
type Entity struct {
	// Type is the Cedar entity type name: "Principal", "FilePath", "Host",
	// "GitRepository", "Tool", etc.
	Type string
	// ID is the entity's identifier string.
	ID string
	// Attributes are the entity's attribute values. Supported Go types:
	//   bool, int64, string → Cedar Bool, Long, String
	//   map[string]any → Cedar Record
	//   []string → Cedar Set<String>
	Attributes map[string]any
}

// Query is one Cedar authorization request.
type Query struct {
	Principal Entity
	// Action is the full Cedar action UID, e.g. `Lki::Action::"Filesystem::Read"`.
	Action   string
	Resource Entity
	// Context carries intent-level attributes: intent_name, intent_version,
	// purpose, and action-specific fields (port, protocol, recursive, etc.).
	Context map[string]any
}

// Decision is the result of a Cedar authorization query.
type Decision struct {
	// Allowed is true if Cedar returned Allow; false for Deny.
	Allowed bool
	// DeterminingPolicies lists policy IDs that contributed to the decision.
	// For Allow: the permit policies that matched.
	// For Deny: the forbid policies that fired.
	DeterminingPolicies []string
	// Diagnostics contains human-readable explanation strings for the decision.
	// Used by the quill for thwart-message generation.
	Diagnostics []string
}

// LoadSchema parses Cedar schema text. The schema text is the content of a
// .cedarschema file (as produced by cedar-from-lki). Currently stores the
// raw text for informational use; cedar-go's experimental schema validator
// is not yet used for request validation.
func LoadSchema(cedarSchemaText string) (*Schema, error) {
	if cedarSchemaText == "" {
		return nil, fmt.Errorf("LoadSchema: schema text is empty")
	}
	return &Schema{raw: cedarSchemaText}, nil
}

// LoadPolicies parses Cedar policy text into an authorization-ready policy set.
// The text may contain multiple policies separated by newlines; all are loaded.
func LoadPolicies(cedarText string) (*Policies, error) {
	if cedarText == "" {
		return nil, fmt.Errorf("LoadPolicies: policy text is empty")
	}
	list, err := cedar.NewPolicyListFromBytes("policies.cedar", []byte(cedarText))
	if err != nil {
		return nil, fmt.Errorf("LoadPolicies: parse Cedar policies: %w", err)
	}
	ps := cedar.NewPolicySet()
	for i, p := range list {
		id := cedar.PolicyID(fmt.Sprintf("policy%d", i))
		if v, ok := p.Annotations()["id"]; ok {
			id = cedar.PolicyID(v)
		}
		if !ps.Add(id, p) {
			return nil, fmt.Errorf("LoadPolicies: duplicate policy id %q", id)
		}
	}
	return &Policies{ps: ps}, nil
}

// Evaluate runs a single Cedar authorization query against the policies.
// The schema parameter is retained for API completeness; in v0.1 evaluation
// proceeds without schema-based validation (cedar-go's validator is experimental).
func Evaluate(_ *Schema, policies *Policies, query Query) (Decision, error) {
	if policies == nil || policies.ps == nil {
		return Decision{}, fmt.Errorf("Evaluate: policies is nil")
	}

	// Build the Cedar request
	req, err := buildRequest(query)
	if err != nil {
		return Decision{}, fmt.Errorf("Evaluate: build request: %w", err)
	}

	// Build entity map from the query's principal and resource
	entities, err := buildEntities(query)
	if err != nil {
		return Decision{}, fmt.Errorf("Evaluate: build entities: %w", err)
	}

	// Run the Cedar authorizer
	decision, diag := cedar.Authorize(policies.ps, entities, req)

	// Extract determining policies
	var determining []string
	for _, reason := range diag.Reasons {
		determining = append(determining, string(reason.PolicyID))
	}

	// Collect diagnostic messages
	var diagnostics []string
	for _, e := range diag.Errors {
		diagnostics = append(diagnostics, fmt.Sprintf("policy %s: %s", e.PolicyID, e.Message))
	}

	return Decision{
		Allowed:             decision == cedar.Allow,
		DeterminingPolicies: determining,
		Diagnostics:         diagnostics,
	}, nil
}

// buildRequest constructs a cedar.Request from a Query.
func buildRequest(q Query) (cedar.Request, error) {
	principal := cedar.NewEntityUID(cedar.EntityType(q.Principal.Type), cedar.String(q.Principal.ID))
	resource := cedar.NewEntityUID(cedar.EntityType(q.Resource.Type), cedar.String(q.Resource.ID))

	// Parse action UID from the string representation
	// Format: "Lki::Action::\"Filesystem::Read\"" or "Filesystem::Read"
	actionUID, err := parseActionUID(q.Action)
	if err != nil {
		return cedar.Request{}, fmt.Errorf("parse action UID %q: %w", q.Action, err)
	}

	// Build context record
	ctx, err := mapToRecord(q.Context)
	if err != nil {
		return cedar.Request{}, fmt.Errorf("build context: %w", err)
	}

	return cedar.Request{
		Principal: principal,
		Action:    actionUID,
		Resource:  resource,
		Context:   ctx,
	}, nil
}

// buildEntities constructs an entity map from the query's principal and resource.
func buildEntities(q Query) (cedar.EntityMap, error) {
	// Build entity JSON and unmarshal into cedar.EntityMap
	type entityJSON struct {
		UID     map[string]string `json:"uid"`
		Attrs   map[string]any    `json:"attrs"`
		Parents []any             `json:"parents"`
	}

	entities := []entityJSON{
		{
			UID:     map[string]string{"type": q.Principal.Type, "id": q.Principal.ID},
			Attrs:   q.Principal.Attributes,
			Parents: []any{},
		},
	}

	// Add resource entity if different from principal
	if q.Resource.Type != q.Principal.Type || q.Resource.ID != q.Principal.ID {
		entities = append(entities, entityJSON{
			UID:     map[string]string{"type": q.Resource.Type, "id": q.Resource.ID},
			Attrs:   q.Resource.Attributes,
			Parents: []any{},
		})
	}

	data, err := json.Marshal(entities)
	if err != nil {
		return nil, err
	}

	var em cedar.EntityMap
	if err := json.Unmarshal(data, &em); err != nil {
		return nil, fmt.Errorf("unmarshal entities: %w", err)
	}
	return em, nil
}

// parseActionUID converts a Cedar action string to an EntityUID.
// Accepts both:
//   - "Lki::Action::\"Filesystem::Read\""  (quoted Cedar literal)
//   - "Filesystem::Read"  (bare action name, uses "Lki::Action" type)
func parseActionUID(action string) (cedar.EntityUID, error) {
	// If it looks like a fully qualified Cedar UID, extract parts
	// Pattern: "Namespace::EntityType::\"ID\""
	// We'll use a simple approach: try to parse as JSON by converting quotes
	// The simplest approach: look for :: pattern
	if len(action) > 0 {
		// Try "Type::\"ID\"" format by detecting the type+id split
		// Find last "::" before the quoted ID
		idx := -1
		for i := len(action) - 1; i >= 1; i-- {
			if action[i] == '"' && action[i-1] == ':' && i >= 2 && action[i-2] == ':' {
				idx = i - 2
				break
			}
		}
		if idx >= 0 {
			typePart := action[:idx]
			// Extract ID from quoted portion: ":\"Filesystem::Read\""
			idPart := action[idx+2:]
			if len(idPart) >= 2 && idPart[0] == '"' && idPart[len(idPart)-1] == '"' {
				idPart = idPart[1 : len(idPart)-1]
				return cedar.NewEntityUID(cedar.EntityType(typePart), cedar.String(idPart)), nil
			}
		}
		// Bare action name: "Filesystem::Read" → Lki::Action::"Filesystem::Read"
		return cedar.NewEntityUID("Lki::Action", cedar.String(action)), nil
	}
	return cedar.EntityUID{}, fmt.Errorf("empty action string")
}

// mapToRecord converts a map[string]any to a Cedar Record for use as context.
func mapToRecord(m map[string]any) (cedar.Record, error) {
	if len(m) == 0 {
		return cedar.NewRecord(cedar.RecordMap{}), nil
	}

	// Use JSON round-trip to convert Go values to Cedar values
	data, err := json.Marshal(m)
	if err != nil {
		return cedar.Record{}, fmt.Errorf("marshal context: %w", err)
	}

	// cedar-go's Record doesn't have JSON unmarshal; build manually
	rm := cedar.RecordMap{}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return cedar.Record{}, err
	}
	for k, v := range raw {
		cv, err := goToCedar(v)
		if err != nil {
			return cedar.Record{}, fmt.Errorf("context key %q: %w", k, err)
		}
		rm[cedar.String(k)] = cv
	}
	return cedar.NewRecord(rm), nil
}

// goToCedar converts a Go value to a cedar.Value.
func goToCedar(v any) (cedar.Value, error) {
	switch val := v.(type) {
	case bool:
		return cedar.Boolean(val), nil
	case int:
		return cedar.Long(val), nil
	case int64:
		return cedar.Long(val), nil
	case float64:
		return cedar.Long(int64(val)), nil // JSON numbers become float64
	case string:
		return cedar.String(val), nil
	case nil:
		return cedar.String(""), nil // Cedar has no null; use empty string
	case map[string]any:
		rm := cedar.RecordMap{}
		for k, inner := range val {
			cv, err := goToCedar(inner)
			if err != nil {
				return nil, err
			}
			rm[cedar.String(k)] = cv
		}
		return cedar.NewRecord(rm), nil
	case []any:
		// Treat as Set of Cedar values
		items := make([]cedar.Value, 0, len(val))
		for _, item := range val {
			cv, err := goToCedar(item)
			if err != nil {
				return nil, err
			}
			items = append(items, cv)
		}
		return cedar.NewSet(items...), nil
	case []string:
		items := make([]cedar.Value, len(val))
		for i, s := range val {
			items[i] = cedar.String(s)
		}
		return cedar.NewSet(items...), nil
	default:
		return cedar.String(fmt.Sprintf("%v", v)), nil
	}
}
