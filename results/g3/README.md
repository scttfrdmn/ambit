# G3 results: live agenkit swarms (H-scale)

- **Registration:** issue #21, with its amendments (scaled-server secondary,
  instance types, the N = 10,000 instrument defect).
- **Write-up:** the G3 report comment on #21, with confounds first.
- **Policies:** `../../corpus/g2c` (hash `1333d39b…`); notes drawn from the
  #55-fixed templates.

## What is here

- `swarm-report.md` and `swarm-report.json.gz`: H-scale parts and pairs,
  latency by N, critical paths by depth, correctness by policy and call kind,
  depth breaches, cap overshoot, cost and auditability. Intervals are 95%
  bootstraps over tasks (2,000 resamples).
- One directory per scored swarm run, each holding:
  - `manifest.json`: arm, N, script hash, seed, slots, instance type, git
    commit, errors;
  - `calls-000.jsonl.gz`: one line per gate call (decision, latency, depth,
    record, oracle truth);
  - `tasks-000.jsonl.gz`: one line per task (critical path by depth);
  - `quill-000.jsonl.gz`: one LKI quill entry per Cedar decision (Cedar arms
    only).
- `superseded/`: manifests only, for the six first N = 10,000 runs that
  failed the registered >1% error rule because of client timeouts (fixed in
  #63 and re-run).

Reproduce the report from this directory alone:

    go run ./cmd/ambit swarm-report -results results/g3

The output matches `swarm-report.md` byte for byte.

Raw model replies (`raw.jsonl`) stay on the experiment's results disk; they
are not in git.

## Read this first

- B's p50 is the structural path: about 65% of Cedar calls need no model. The
  margin tail grows with N.
- A's latency at scale is queueing at one shared model server per arm.
- A-d3 at N = 10,000 was not run: no g6e.xlarge capacity. No registered
  verdict uses it.
