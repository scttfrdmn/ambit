# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Project scaffold: license, CLAUDE.md with the experiment's laws,
  substrate brief, Makefile targets.
- Vendored LKI `pdp` and `quill`. `LoadPolicies` keys policies by `@id`.
- Five policies, as Cedar and as operator prose (`corpus/policies/`).
- Oracle implementing the prose in Go, independent of Cedar.
- Seeded corpus generator:
  - 640 base records in 80 scenario families
  - 160 injected negative controls (allow, escalate, redirect)
  - 200 presentation variants (reordered tools, opaque names, reviewer note)
- `make corpus`, which runs these checks before writing:
  - cross-check of Cedar against the oracle, with and without semantic facts
  - Law 2 check
- `make verify`.
- Mutation tests showing that the cross-check catches divergence between
  Cedar and prose.
- Arm gates:
  - B0: Cedar over structural facts.
  - B: Cedar plus a PIP, asked only at the margin and only about the payload.
  - A: a decision model over the serialized agent state.
- Go client for Jebadiah GGUF models on llama-server. It matches the model
  card's published output.
- Thin OpenAI Decisions client with a spend ledger that enforces the
  per-run and project caps.
- `ambit run` writes manifest, decisions, raw replies, and LKI quill entries
  to an external results directory.
- `ambit report` reports per policy and condition with family bootstrap
  intervals, flip rates, latency, cost, auditability, PIP accuracy, and
  hypothesis verdicts.
- `ambit state`, and the `make model`, `make serve`, `make run` and
  `make report` runbooks.
- Arms A-strands and B-strands (library): a client for strands-decider's
  `/v1/systemone`, pinned to strands-decider-2B-hobson-v21, that sends one
  request at a time; the agent state in strands-decider's tool-call hook
  layout, with arguments byte-identical to Python's `json.dumps`; the
  A-strands gate, which sends the hook-style and shared gate questions in
  one request; and a strands-decider PIP for the Cedar arm.
- G2 corpora:
  - 10^5 and 10^6 records, stored as gzipped 100k-record shards. Hashes
    cover the decompressed content, so G0's hash is unchanged.
  - New conditions: patient-header twins and swarm traces (cost,
    delegation, relay).
  - `ambit corpus -config g0|g2a|g2b`.
- Runner at scale:
  - `-offset`/`-limit` shard ranges and `-concurrency`.
  - Streaming reads of sharded corpora.
  - Manifest records the range, concurrency, host, prompt caching, prose
    hash and ablation.
  - Falls back to a `GIT_COMMIT` file where there is no repository.
- `-cache-prompt` for llama-server prefix reuse, checked for equivalence
  before use.
- `-ablation failclosed`, the registered Arm A prose ablation.
- `-ceiling`: the hosted spend ceiling per results directory, one per gate.
  The spend ledger is locked per process.
- Hosted retries with backoff on rate limits, 5xx and transport errors, up
  to 10 attempts; never retried on quota or auth errors.
- `-repair` re-asks only transport or capacity failures. The report
  substitutes those repairs for errors and for nothing else.
- Arms `a-strands` and `b-strands` in the CLI (`-strands`,
  `-strands-concurrent`).
- Report:
  - Merges shard runs into exact covers, and refuses shards that disagree
    on prose or models.
  - Builds conditions dynamically.
  - Plots Arm A operating curves.
  - H-main at a matched clean false-deny, with the 10% headline bound and
    a label for matches with no comparable operating point.
  - Header-pair flips, with B's header-added cells marked structural.
  - Swarm-trace accuracy and relay breach rate.
  - Within-ambit accuracy per arm.
  - Refusals shown as structural for arms that cannot refuse.
  - Law 3 checks the arms that share a serializer.
  - The registered G2b analysis plan (#33):
    - `-transfer-from`: the threshold transfer test, Arm A at a prior
      report's matched t.
    - A full-procedure bootstrap on the matched comparison that re-selects
      t in every resample.
    - Ratio intervals for B / A, and Holm-adjusted one-sided p-values.
    - Exploratory B-arm curves over the PIP acceptance threshold, and
      calibration tables.
    - `-replicate`: a replication table against a prior report.
- `ambit compute-cost`: GPU cost of the self-hosted arms per 10k decisions,
  from each run's wall time and the GPU its instance's job log names, at
  on-demand list price (#33).
- G2b results at 10⁶ (`results/g2b/`): manifests, decisions, the report,
  GPU cost, and the manifests of quarantined shards.
- G2c results at 10⁵ (`results/g2c/`), the #55-corrected confirmation
  registered on #56: manifests, decisions, the report and GPU cost.
- `gpu-job.sh` runs G3 swarms: a RUNS line starting `swarm` runs
  `ambit swarm` with the instance type and the model server's slots
  recorded. `#llama-np N` and `#d3-np N` set server slots for the
  scaled-server secondary; by default the slots are G2's. The watchdog
  counts streamed results as progress (#46).
- `scripts/spore`, which runs work on spore.host:
  - Stages a bundle and a runs file, then launches spot GPU instances with
    a TTL, a cost limit of at most $20, a `project=ambit` tag and a scoped
    S3 grant.
  - Instance job with pinned llama.cpp and strands-decider runtimes, logs
    streamed to S3, and a no-progress watchdog.
  - Local dead-man watcher, fetch, and straggler check.
- G3 live-swarm harness (`internal/swarm`, #21), ported to main (#44):
  - A seeded script of N agents in one tree per task, down to depth 3,
    each making K work calls drawn with the corpus's scenario builders.
    The registered seed 20261021, K = 4 and default shape give 48 / 496 /
    5,229 / 52,119 scripted gate calls at N = 10 / 100 / 1,000 / 10,000.
  - Agents are agenkit agents and delegation is an agenkit AgentTool. The
    principal chain travels in the context. The gate is an agenkit.Tool
    decorator that records each decision with the oracle's verdict on the
    facts the gate saw.
  - Task and swarm spend accrue live, so a cap can be crossed mid-run.
  - Per-task critical paths, split by depth. Gate latency excludes the
    in-flight wait.
  - Cedar arms write one LKI quill entry per gate decision to the run's
    `quill.jsonl` (#45). Repairable errors are counted apart.
- `ambit swarm`: any arm `ambit run` takes, with the same gate flags and
  the same gate construction (`cmd/ambit/gates.go`). Hosted arms stop at
  the ledger's per-run cap and ceiling, as `ambit run` does. `-serial`
  makes reference runs. `-instance-type` and `-server-slots` are recorded
  in the manifest.
  - Results stream as the run goes, as `ambit run`'s do: the manifest is
    written unfinished at the start and rewritten at the end, and one
    writer goroutine appends each call to `calls.jsonl`, its replies to
    `raw.jsonl` and its quill entry, in record order, flushing at least
    every second. A run killed by a TTL stop, SIGTERM or OOM keeps what it
    recorded. SIGTERM is handled like an interrupt: the swarm halts and
    the manifest is rewritten stopped, with `finished` left empty.
  - Recording never blocks on the writer while holding the harness lock.
- `ambit swarm-report`, implementing the G3 registration on #21:
  - B flat (p50 at N = 10,000 over N = 100), A compounds (critical-path
    slope on depth at N = 1,000, paired by task against every B arm), and
    the p50 thresholds at N = 1,000, all on 95% task-bootstrap bounds with
    2,000 resamples.
  - All 25 (B, A) pairs, each part given separately, with the serial
    reference's probe resolution beside them.
  - Runs whose repairable errors exceed 1% of gate calls are flagged as
    instrument failures and decide nothing.
  - Accuracy by policy, ambit class and call kind; A-d3 at its
    G2b-transferred threshold (exploratory); depth breaches; cap overshoot;
    cost per run and per 1k gate calls; denials with a determining policy.
  - Scaled-server runs (more server slots than the arm's primary runs) go
    in a separate secondary table, outside the verdicts.
  - Only the ten registered arms enter verdicts. Other arms (G1's `b`,
    ablations, `-name` runs) are listed under "Other runs (no verdict)".
  - An A arm is not paired with a B arm whose run at N = 1,000 ran a
    different script: its compounds part is "not measured (script
    differs)".
  - Incomplete runs (stopped, or with an unfinished manifest) are listed
    with the calls they streamed and enter no verdict or table.
- `-run-cap` for `ambit run` and `ambit swarm`: the per-run hosted cap in
  dollars, default 5. Values at or below 0 or above 15 are refused. The
  cap is recorded in the manifest (`run_cap_micros`).

### Changed

- `ambit run`'s gate construction moved to `cmd/ambit/gates.go`, which it
  shares with `ambit swarm`. Its flags and behaviour are unchanged, apart
  from the new `-run-cap`.
- Determining policies are sorted, so decisions and quill entries are
  reproducible.
- The G1 report, regenerated with the current code:
  - It adds the B0-coverage and matched-point H-main sections and labels
    the registered H-main as the G1 metric.
  - Arms that cannot refuse show "cannot refuse (structural)".
  - The G1 numbers are unchanged.

### Fixed

- G3 swarms no longer turn queueing into gate errors (#21). At N = 10,000 with
  no in-flight cap, the `ambit run` client limits (llama 10 min, strands
  2 min, Bedrock 2 min with 10 attempts, Decisions 10 attempts) failed up to
  64% of calls. `ambit swarm` now lets each call wait up to 6 h, with up to
  1,000 retries, so its registered latency includes the wait. `ambit run` is
  unchanged.

- Generator: two cv_outcomes on-subject note templates were ambiguous
  against their proposition (#55). An event-free follow-up and an MI that
  predates therapy were labelled "about major adverse cardiovascular events
  in patients on lipid-lowering therapy". `NoteRevision` 1 replaces them
  with notes that record a MACE on therapy. Revision 0 still reproduces the
  frozen G0, G2a and G2b corpora byte for byte. New corpus `corpus/g2c`
  (hash `1333d39b…`) is G2a regenerated with the fix: 1,926 records differ,
  exactly the #55 set, and the oracle and policies are unchanged.

- spore.host fleet defects found during G2 (#33):
  - A shard counts as done only if it has no repairable gate errors. A
    crashed model server no longer passes as 5,000 decided records. Run
    manifests now count repairable errors separately. A shard that finishes
    three times with repairable errors is given up on, not relaunched
    forever.
  - `fetch.sh` fetches outputs from every region, not the first region that
    has a stage.
  - A launch that spawn reports as failed is swept for any instance it
    created (`sweep.sh`). `try-launch.sh` sweeps the other regions after a
    success, and the dead-man watcher checks every region. `launch.sh`
    refuses a run name that is already live, and passes
    `--terminate-on-error`, so spawn terminates an instance whose spored never
    came up (the root cause of the G2 orphan, #50).
