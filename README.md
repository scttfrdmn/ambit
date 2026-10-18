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

Generates the frozen corpus under `corpus/` and writes its SHA-256 hash.

### `make run ARM=b0|b|a-local|a-hosted`

Runs one arm over the frozen corpus. Results go under `results/`.

### `make report`

Computes the measures with bootstrap CIs from `results/`.

### `make test`

Runs the unit tests.

## License

Apache 2.0.
