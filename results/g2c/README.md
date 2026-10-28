# G2c results (10⁵, corrected for #55)

- **Corpus:** `../../corpus/g2c`, hash
  `1333d39b1cb9e2189639b7742e03d6d3481c448a2b4470c1387147510f8b8348`.
  It is G2a's configuration with the #55 note fix (generator `NoteRevision`
  1). It differs from G2a in exactly the 1,926 records built from the two
  ambiguous cv_outcomes templates; the oracle and policies are identical.
- **Registration:** issue #56, made before any inference.
- **Write-up:** the G2c report comment on #56, with confounds first.
- G2's registered results (`results/g2a`, `results/g2b`) stay on record
  unchanged. G2c is "G2, corrected for #55 (Law 1)".

## What is here

- `report.md` and `report.json.gz`: per arm, per policy and per condition,
  with 95% family-bootstrap intervals (2,000 resamples).
- `compute-cost.md`: GPU cost of the self-hosted arms, at on-demand list
  price.
- One directory per scored run, each holding:
  - `manifest.json`: corpus hash, git commit, models and pins, range,
    concurrency and host;
  - `decisions-000.jsonl.gz`: one decision per record.

Reproduce the report from this directory alone:

    go run ./cmd/ambit report -dir corpus/g2c -results results/g2c

The output matches `report.md`. The original build, made from the full
results disk, also listed one A-hosted shard that stopped when OpenAI credit
ran out (#59) and was re-run in full. Every number is identical.

Raw model replies (`raw.jsonl`), LKI quill entries and per-instance job logs
stay on the experiment's results disk; they are not in git.

## Read this first

- Almost all of the Cedar arms' remaining clean false-denies are on one
  replacement note, "Admitted with NSTEMI after two years on rosuvastatin…".
  The PIPs do not infer that an NSTEMI on a statin is a major adverse
  cardiovascular event on lipid-lowering therapy. A-d3 is matched to that
  false-deny rate.
- H-main moves Arm A's threshold to Cedar's clean false-deny. Cedar's PIP
  threshold stays at the registered 0.5.
- A-hosted has no comparable operating point.
- The safeguard arms are hard-label arms. Each has one operating point.
