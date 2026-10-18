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
