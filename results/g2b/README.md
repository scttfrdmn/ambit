# G2b results (10⁶)

- **Corpus:** `../../corpus/g2b`, hash
  `f5fdd0309581055711be01177fab0ed18be7d953f12ce9168f02735bd2389a02`.
- **Write-up:** issue #17, the G2 gate report comment, with confounds first.
- **Registrations:** issue #33, including the G2b analysis plan and the
  instrument defects found during the runs.

## What is here

- `report.md`: per arm, per policy and per condition, with 95%
  family-bootstrap intervals (2,000 resamples). It includes the transfer test
  against G2a's thresholds and the replication table.
- `report.json.gz`: the same, as data. Gunzip it to pass to
  `-transfer-from` or `-replicate`.
- `compute-cost.md`: GPU cost of the self-hosted arms, at on-demand list price.
- One directory per scored run, each holding:
  - `manifest.json`: corpus hash, git commit, models and pins, range,
    concurrency and host;
  - `decisions-000.jsonl.gz`: one decision per record.
- `quarantine/`: manifests only, for the 10 A-strands shards whose
  strands-decider server crashed. Every decision in them is an error. They
  were re-run in full and are not scored (#33).

Reproduce the report from this directory alone:

    go run ./cmd/ambit report -dir corpus/g2b -results results/g2b \
        -transfer-from results/g2a/report.json -replicate results/g2a/report.json

The output matches `report.md`. The original build, made from the full
results disk, also listed the runs that are not scored here: incomplete
shards, runs on the 10⁵ corpus, and one A-strands range covered twice by a
fleet relaunch. Every number is identical.

These stay on the experiment's results disk and are not in git:
- raw model replies (`raw.jsonl`);
- LKI quill entries (`quill.jsonl`);
- per-instance job logs. `compute-cost.md` was built from these logs.

## Read this first

- H-main moves Arm A's threshold to Cedar's clean false-deny. Cedar's PIP
  threshold stays at the registered 0.5.
- A-d3's thresholds are high (0.90–0.96) and were carried over from G2a
  unchanged.
- A-hosted has no comparable operating point.
- The safeguard arms are hard-label arms. Each has one operating point.
