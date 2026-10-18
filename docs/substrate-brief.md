# Substrate brief

What ambit takes from LKI, agenkit-go, and aws-agentcore-demo, and what it has
to build itself. Read from source on 2026-10-17.

## LKI (`~/src/lki`, module `github.com/playgroundlogic/lki`, go 1.23.0)

**Not importable.** `pdp`, `cedar`, and `quill` sit under `internal/`. The only
exported package, `gauntlet`, takes an `*flightplan.CastPlan` that no exported
function produces. A `replace` directive does not get around the internal rule.

**Decision (#1):** vendor `internal/pdp/pdp.go` (depends only on cedar-go) and
`internal/quill/{types,emit}.go` (stdlib only) into `internal/lki/`. Keep the
upstream path and commit in a header comment. Arm B is "LKI's PDP and audit
format", not LKI's cast loop. The cast loop computes its own facts and
offers no PIP hook.

### pdp

```go
type Entity   struct { Type, ID string; Attributes map[string]any }
type Query    struct { Principal Entity; Action string; Resource Entity; Context map[string]any }
type Decision struct { Allowed bool; DeterminingPolicies []string; Diagnostics []string }
func LoadPolicies(cedarText string) (*Policies, error)
func Evaluate(_ *Schema, p *Policies, q Query) (Decision, error)   // schema is not validated
```

- Cedar library: `github.com/cedar-policy/cedar-go v1.6.1`.
- Only the principal and the resource become entities, with no parents. The
  principal chain and the facts therefore go in attributes or in `context`.
- A bare action name `X` becomes `Lki::Action::"X"`.
- `goToCedar` turns float64 into Long by truncating, and nil into `""`.
  **Ambit therefore keeps every number an integer:** cost in micro-dollars,
  depth as an int. Missing facts are left out, never sent as nil.
- `DeterminingPolicies` come from Cedar's reasons. Without `@id` they are
  `policy0`, `policy1`, and so on. **Every ambit policy carries `@id`** so that a
  quill names its rule (H-audit).
- A Deny is not an error. Errors come only from build failures.

### quill

- One JSONL `Entry` per decision (`schema_version` "0.2"). It holds principal,
  intent, `observed_state map[string]map[string]any`, and `pdp_decisions[]`,
  each with the Cedar request, the decision, `determining_policies`,
  `determining_reason`, and `evaluation_duration_ms`.
- `entry_id = "sha256:" + hex(SHA256(sorted-key JSON))`. There is no chain and
  no verify function.
- Ambit records PIP-supplied facts, with their confidences, in
  `observed_state["pip"]`. Upstream cast never fills that field.

### Reference test

`internal/resolver/demo_test.go:157` `TestDemoScenario_WithRealResolver`:

1. Compiles a FlightPlan into a CastPlan.
2. Runs a 7-step scripted agent (read → write → remove ×2 → commit) through
   PDP+quill against a temp git workspace.
3. Asserts the effects on the filesystem.

It does not assert quill contents. Ambit's tests do.

### LKI format

A spec is one YAML per versioned intent (`specs/cli/<intent>/vN.0.0.yaml`). It
covers signature, behavior prose, constraints, capability needs, and
implementation. Cedar is derived from it, with three tiers: safety floor,
baseline, and tenant. Entity attributes are *structural* (guaranteed by type)
or *observed* (per request). This is the same split as ambit's structural
facts versus PIP facts.

## agenkit-go (`~/src/agenkit/agenkit-go`)

- **Import path:** use the mirror `github.com/scttfrdmn/agenkit-go`. The
  in-tree `github.com/scttfrdmn/agenkit/agenkit-go` is deprecated. Never mix
  the two paths, because the types diverge.
- **Core interfaces:** `Agent{Name, Process(ctx,*Message), Capabilities,
  Introspect}` and `Tool{Name, Description, Execute(ctx, map[string]any)
  (*ToolResult, error)}`.
- **Patterns** (`patterns/`):
  - Supervisor (delegation)
  - AgentTool (agents as tools, i.e. hierarchy)
  - ParallelAgent (fan-out)
  - Sequential, Router, ReAct, ReasoningWithTools

  There is no swarm or handoff pattern. Note that MultiAgentOrchestrator
  ignores its strategy argument and always runs sequentially.
- **No principal anywhere:** none on agents, messages, or tools.
  `AgentTool.Execute` builds a new Message, which drops metadata.
  **Decision (#1):** the principal chain travels in `context.Context`. Each
  delegation appends to it.
- **No tool-middleware hook.** **Decision (#1):** the gate (PEP) is an
  `agenkit.Tool` decorator. Its `Execute` builds the invocation record, asks
  the arm, then either calls `inner.Execute` or returns a denial. One wrapper
  covers ReAct, ToolAgent, MCP, and AgentTool delegation.
- G0–G1 does not run live agents. The gate is driven from the frozen corpus.
  The decorator and ctx chain matter from G3.

## aws-agentcore-demo (`~/src/aws-agentcore-demo`)

**Policies.** The demo has only two, written inline in `build_kb.py:1153-1201`:

```cedar
permit(principal, action, resource == AgentCore::Gateway::"<arn>");                       // PermitAll
forbid(principal, action == AgentCore::Action::"web-tools___web_fetch", resource == ...); // ForbidWeb
```

**Lessons carried into ambit:**

- Match on the action name, not on a `context.toolName` condition. The
  condition never fired in the demo.
- AgentCore's action form is `{target}___{tool}`, with three underscores.

**Gaps:**

- No schema: AgentCore generates one.
- No principals: the authorizer is `NONE`.
- One tool: `web_fetch` against the ClinicalTrials.gov v2 studies API, with
  parameters `query.term`, `filter.overallStatus`, `pageSize`, `format`, and
  `countTotal`.

The "Q4 Cedar denial" is `question_5()` in current code; the beats were
renumbered.

**What ambit takes:** the PCSK9 research setting, `web_fetch` with its
parameters, the ForbidWeb shape (egress policy 2), and denial as a typed
outcome distinct from error. Everything else is new in ambit: principals,
roles, PHI datasets, cost, delegation, and the other tools. That vocabulary
is defined once in `internal/vocab` and shared by the generator, Cedar, and
the Arm A serializer.
