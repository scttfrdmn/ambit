# ambit

An experiment, not a product.

**Question.** For gating tool invocations by AI agents, which kind of policy
decision point (PDP) does better? The two candidates:

- **Cedar PDP over runtime facts.** A small model acts only as a policy
  information point (PIP).
- **Decision model as the PDP.**

They are compared on false-allow rate, flip rate under injection, critical-path
latency, cost, and auditability.

**Vocabulary.**

- *Within ambit:* Cedar resolves the decision on structural facts alone.
- *At the margin:* a rule applies, but the PIP must supply a fact.
- *Outside ambit:* no rule covers the decision. The result is default-deny or
  escalate, never a model's allow.

| Arm | What decides |
| --- | --- |
| B0 | Cedar over structural facts only |
| B | Cedar over structural facts + a 4B PIP for payload facts |
| A-local / A-hosted | a decision model over serialized agent state |

The corpus is generated, and the generator is the oracle. Planning and
results live in the issues and in GitHub Project #76.

## Runbooks

Requires Go 1.27+.

### `make corpus`

Generates the 1,000-record corpus from a fixed seed into `corpus/`:

- `records.jsonl` holds what a gate may see.
- `oracle.jsonl` holds the ground truth. Arms never read it.

It writes `SHA256SUMS` and prints the corpus hash, which is the SHA-256 of
`SHA256SUMS`. Before writing anything it checks two things, and it fails if
either check fails:

- **Cross-check.** Cedar (`corpus/policies/ambit.cedar`) agrees with the
  oracle on every record, both with the true semantic facts and with none.
  The oracle is the prose in `ambit.md`, implemented in Go.
- **Law 2.** Every injected record is denied.

The output is deterministic, so rerunning reproduces the same hash.

### `make verify`

Confirms that the corpus files still match `SHA256SUMS` and prints the hash.

### `make model` and `make serve`

Arm B's PIP and Arm A-local both use Jebadiah 4B v2. The weights are pinned
by Hugging Face revision and file SHA-256.

1. Install llama.cpp 0.7.0 or later: `brew install llama.cpp`.
2. Run `make model`. It downloads the pinned GGUF to the external disk
   (`~/.xtmp/ambit-models`) and checks its hash.
3. Run `make serve` in a separate terminal. It serves the model on
   127.0.0.1:8080.
4. Optional: run `AMBIT_LLAMA_URL=http://127.0.0.1:8080 go test ./internal/jeb/`.
   This checks the Go client against the output published on the model
   card.

### `make run ARM=...`

Runs one arm over the frozen corpus. A run refuses to start if the corpus
does not match `SHA256SUMS`.

| ARM | Gate | Needs |
| --- | --- | --- |
| `b0` | Cedar over structural facts only | nothing |
| `b` | Cedar + Jebadiah 4B PIP, asked only at the margin, on the payload only | `make serve` |
| `b-hosted` | Cedar + OpenAI Decisions as the PIP (for H-pip) | `OPENAI_API_KEY` |
| `a-local` | Jebadiah 4B over the serialized agent state | `make serve` |
| `a-hosted` | OpenAI Decisions `gpt-6-luna` over the same state | `OPENAI_API_KEY` |

Outputs go to `$(RESULTS)/<arm>-<timestamp>/`. `RESULTS` defaults to
`~/.xtmp/ambit-results`, on the external disk. Each run directory holds:

- `manifest.json`: corpus hash, git commit, model pins, and spend.
- `decisions.jsonl`: one decision per record.
- `raw.jsonl`: every model reply, verbatim.
- `quill.jsonl`: one LKI audit entry per Cedar decision.

Hosted calls are checked against two caps before they are made: $5 per run
and $200 for the project, read from `$(RESULTS)/spend.jsonl`. A run stops
rather than crossing either cap. Use `-limit n` for a smoke test.

`ambit state <record-id>` prints the exact text every Arm A model receives
for a record.

### `make report`

Scores the latest complete run of each arm against the oracle and writes
`report.md` and `report.json` to `$(RESULTS)`. Every rate is reported per
policy and per condition (clean, injected, and each variant), with 95%
bootstrap intervals over scenario families. The report also covers:

- flip rates against each arm's own decision on the base record;
- latency, cost, and auditability;
- PIP fact accuracy;
- the registered hypotheses.

### `ambit swarm` and `ambit swarm-report` (G3)

`ambit swarm -arm b0 -agents 1000` runs a live agenkit swarm of N agents
with every tool call gated by the arm. The swarm is a seeded script (the
registered seed 20261021 and K = 4 by default): one tree per task, down to
depth 3, with K work calls per agent. It takes every arm and gate flag
`ambit run` takes, built by the same code. `-concurrency n` caps the gate
calls in flight (0, the default, sets no cap); `-serial` makes an
uncontended reference run; `-instance-type` and `-server-slots` are
recorded for the cost and the scaled-server secondary. Hosted arms spend
against `<results>/spend.jsonl` under `-run-cap` (dollars, default 5, at
most 15) and `-ceiling`, as `ambit run` does. Outputs go to
`<results>/<arm>-swarm-<N>[-serial]-<timestamp>/`:

- `manifest.json`: script hash, policy corpus hash, git commit, models,
  N, K, seed, concurrency, throughput, errors (repairable apart), spend.
- `calls.jsonl`: one line per gated call, with the record the gate saw,
  its decision and latency, and the oracle's verdict.
- `tasks.jsonl`: each task's critical path, split by depth.
- `quill.jsonl`: one LKI quill entry per Cedar decision (B0, B arms).
- `raw.jsonl`: every model reply, verbatim.

`ambit swarm-report -results dir` writes `swarm-report.md` and
`swarm-report.json`: the registered H-scale rules of #21 per part and per
(B, A) pair, gate latency by N, critical path by depth, accuracy against
the oracle, depth breaches, cap overshoot, cost, auditability, and any
scaled-server runs apart from the verdicts.

`ambit run` also takes `-run-cap`.

### `make test`

Runs the unit tests.

## License

Apache 2.0.
