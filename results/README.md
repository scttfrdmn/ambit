# G1 results

## Read this first: the H-main metric is defective

The registered H-main metric is false-allow on the injected subset. It
rewards an arm for denying everything. Every injected record should be
denied, so a gate that denies everything scores perfectly. A-hosted denied
838 of 1,000 records, including 60.5% of permitted clean calls. Its low
injected false-allow therefore says almost nothing about resistance to
injection. B0, which allows nothing it cannot prove, also scores 0.

Arm B and B0 have fixed operating points. Arm A has a tunable threshold
on P(allowed). The comparison that answers the question is false-allow on
the injected subset at a matched clean false-deny rate. Sweep Arm A's
threshold, draw its curve, and place B on the same axes. An arm whose
clean false-deny exceeds a declared bound is a wall, not a gate.

The G1 verdict stands as registered. The matched-operating-point metric and
a 10% clean false-deny bound for headline eligibility are registered for
G2, before any G2 inference. See issue #16.

## What is here

- **Corpus:** `d819172f7133679273ccce0f0a57b2fff3440089bf92a7c49235c96371622245`.
  It is in `../corpus/` and is synthetic; no record describes a real person.
- **`report.md` / `report.json`:** the full report. Every arm is broken down
  by policy and by condition, with 95% bootstrap intervals over scenario
  families, plus:
  - flip rates;
  - latency, cost, and auditability;
  - PIP fact accuracy;
  - the hypothesis verdicts.
- **One directory per arm run.** Each holds:
  - `manifest.json`: corpus hash, git commit, model pins, and spend;
  - `decisions.jsonl`: one decision per record;
  - `raw.jsonl`: every model reply, verbatim;
  - `quill.jsonl`: LKI audit entries, for the Cedar arms.
- **`spend.jsonl`:** every hosted call's tokens and cost, $0.1741 in total.
  The total includes a 3-call smoke test that is not part of any run.
- **`probes/`:** one probe of Amazon Bedrock's OpenAI-compatible
  `/openai/v1/decisions` route. It returned HTTP 500, so it was
  inconclusive.

## Interpreting A-hosted

No re-run was warranted: the false denials are neither mostly refusals nor confined to one policy.

- **Exact zeros.** A-hosted returned P(allowed) = 0.0 on 619 of 1,000
  records. Exact zero is its confident-deny output. On records that should
  be denied it is usually right, for example on 117 of 119 deny-truth SQL
  queries.
- **Refusals.** It refused 5 of 1,000 questions. Refusals count as denials
  and are also broken out in their own column. Only 2 of its 179 clean
  false denials are refusals.
- **Where the false denials fall.** They are spread across all five
  policies. They concentrate on payloads with identifier-shaped text: it
  denied 52 of 56 permitted reads of clinical notes, which carry a
  patient header. On SQL its clean false-deny is 1 of 115.

The served model is `gpt-6-luna`, an alias rather than a snapshot. All
calls were made on 2026-10-18.

## Reproducing

See the runbooks in `../README.md`: `make model`, `make serve`,
`make run ARM=...`, and `make report RESULTS=results`. Local arms ran on
an Apple M4 Pro with llama.cpp 0.7.0 and Jebadiah 4B v2 Q8_0, pinned in each
manifest.
