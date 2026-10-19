# G2a results (10⁵)

- **Corpus:** `../../corpus/g2a`, hash
  `8622a1220109dfa93ae28f2840171429d4de54341bd4bfba33ae566a88734862`.
- **Write-up:** issue #17, the G2a report comment, with confounds first.
- **Registrations:** issue #33.

## What is here

- `report.md` and `report.json`: per arm, per policy and per condition,
  with 95% family-bootstrap intervals (2,000 resamples).
- One directory per scored run, each holding:
  - `manifest.json`: corpus hash, git commit, models and pins, range,
    concurrency and host;
  - `decisions-000.jsonl.gz`: one decision per record.

Reproduce the report from this directory alone:

    go run ./cmd/ambit report -dir corpus/g2a -results results/g2a

Raw model replies (`raw.jsonl`) and LKI quill entries (`quill.jsonl`) are
about 2 GB and stay on the experiment's results disk. They are not in git.

## Read this first

- A-safeguard and B-safeguard are hard-label arms. Each has one operating
  point.
- A-hosted has no comparable operating point near any B arm's clean
  false-deny.
- A-d3's matched comparisons sit at thresholds of 0.90–0.96.

See confounds 1–8 in the write-up before reading any H-main cell.
