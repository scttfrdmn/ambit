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
