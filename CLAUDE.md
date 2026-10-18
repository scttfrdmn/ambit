# ambit

An experiment, not a product. Build only what the measurements need.

**Question.** For gating tool invocations by AI agents, including thousands of
coordinating agents, how does a Cedar PDP over runtime facts compare with a
decision model used as the PDP? In the Cedar arm, a small model serves only as
a PIP. The comparison covers five things:

- false-allow rate
- flip rate under injection
- latency on the critical path
- cost
- auditability

**Vocabulary.**

- *Within ambit:* Cedar resolves the decision on structural facts alone.
- *At the margin:* a rule applies, but the PIP must supply a fact.
- *Outside ambit:* no rule covers the decision. The result is default-deny or
  escalate, never a model's allow.

## Arms

- **B0:** Cedar (LKI pdp) over structural facts only. No model.
- **B:** Cedar over structural facts plus a 4B PIP (Jebadiah 4B v2, pinned by
  HF commit). The PIP answers narrow typed questions about the payload only.
- **A-local / A-hosted:** a decision model gets the gate question over
  serialized agent state, in the LangChain middleware pattern.
  - A-local: Decision 3.0 d3 if it fits, else Jebadiah 4B v2.
  - A-hosted: OpenAI Decisions `gpt-6-luna`.

## Laws

1. **Oracle.** The generator's verdict is ground truth. An arm that disagrees
   is wrong. A case that exposes policy ambiguity is a corpus defect: fix
   Cedar and prose together, regenerate, re-hash, and record it in the issue.
2. **Negative controls.** Every injected record must deny under the oracle.
   The run fails if any does not.
3. **Fairness.** Every Arm A model gets the same state. Every Cedar evaluation
   gets the same facts. No tuning after results.
4. **Attribution.** Never report an aggregate alone. Report per policy and per
   condition, with bootstrap CIs over scenario families.
5. **Hygiene.**
   - Freeze the corpus and take its SHA-256 before any inference. Put the
     hash in every result file.
   - Run the cheapest rung first.
   - Before trusting a null result, confirm the probe could have resolved the
     effect.
   - Stream logs off any instance as the run goes.
6. **Spend.** Per-run caps are $5 hosted and $20 on AWS. The project ceiling
   is $200 through G1. Log every hosted call's tokens and cost.
7. **Decide and record.** When a choice depends on domain knowledge Scott
   lacks, or on the project's state, decide, record it in the issue, and
   continue. Escalate only these:
   - the name
   - public pushes
   - G2/G3 budgets
   - adding paid models
   - the real AgentCore Gateway
   - anything touching real data
8. **Authority.** Scott decides scope, names, versions, releases, and when
   anything is done. Report state and outcome only.

## Rules for working here

- Go only; no Python in the core path. Apache 2.0.
- Changelog: Keep a Changelog 1.1.0, `[Unreleased]` only. No tags, releases,
  or version numbers.
- Planning lives in GitHub issues, milestones, labels, and Project #76 only.
  - Allowed local files: README, LICENSE, CHANGELOG, CLAUDE.md, code, tests,
    scripts, `corpus/`, `results/`, and `docs/substrate-brief.md`.
  - No plan, status, or TODO files.
- Hosted calls:
  - Record the served model ID and the date on every call.
  - Save every raw response under `results/`.
- Numbers crossing into Cedar are integers: cost in micro-dollars, depth as an
  int. LKI's pdp truncates floats. Never pass nil; omit missing facts.
- Every Cedar policy carries `@id`, so the determining policy is named.
- LKI's pdp and quill are vendored under `internal/lki/`, because
  `internal/` cannot be imported. See docs/substrate-brief.md.
- Stop after G1 and report.
