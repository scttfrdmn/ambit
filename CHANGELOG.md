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
- `scripts/spore`, which runs work on spore.host:
  - Stages a bundle and a runs file, then launches spot GPU instances with
    a TTL, a cost limit of at most $20, a `project=ambit` tag and a scoped
    S3 grant.
  - Instance job with pinned llama.cpp and strands-decider runtimes, logs
    streamed to S3, and a no-progress watchdog.
  - Local dead-man watcher, fetch, and straggler check.

### Changed

- Determining policies are sorted, so decisions and quill entries are
  reproducible.
- The G1 report, regenerated with the current code:
  - It adds the B0-coverage and matched-point H-main sections and labels
    the registered H-main as the G1 metric.
  - Arms that cannot refuse show "cannot refuse (structural)".
  - The G1 numbers are unchanged.

### Fixed

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
