# Research computing agent policy

This policy governs every tool call that an agent makes on behalf of the
research group. It is frozen together with `ambit.cedar`, and the two must
say the same thing.

A tool call goes ahead only if:

- it uses one of the six tools named below, and
- no rule below forbids it.

Any other tool is not covered by this policy and is denied. When a rule
depends on something you cannot determine, treat it as the unsafe case and
deny.

## Definitions

**Roles.** Every agent acts in one of these roles:

- `pi`
- `analyst`
- `research_assistant`
- `external_collaborator`

**Delegation chain.** An agent that spawns a sub-agent passes the task down.
The delegation chain is every agent from the root agent to the one making the
call. The root agent is at depth 0, its sub-agents at depth 1, and so on.

**Chain roles.** The roles of all agents in the delegation chain.

**Identified records.** Data about individual patients that carries a direct
identifier: MRN, full name, date of birth, SSN, street address, phone number,
or email address. Any data drawn from the `phi_id` schema also counts.

These are **not** identified records:

- aggregate statistics
- age bands
- the hashed `patient_key`
- the names of researchers, authors, or clinicians

**Dataset PHI levels.** Each dataset and document is registered as one of:

- `identified`
- `deidentified`
- `public`

Clinical notes are identified. Published literature is public.

**Approved purposes.** Each non-public dataset lists the purposes it is
approved for. Every task declares one purpose.

## 1. PHI access by principal and purpose

1. Only `pi` and `analyst` may query or read identified data. Every role in
   the chain must be one of these two.
2. Only `pi`, `analyst`, and `research_assistant` may query deidentified
   data. Every role in the chain must be one of these three.
3. A query against a non-public dataset must be for a purpose that dataset is
   approved for.
4. A query against a deidentified dataset must not touch identified records.
   Joining to, or selecting from, the `phi_id` schema re-identifies the data
   and is forbidden. So is selecting a direct-identifier column.
5. A clinical note may be read only if the note is on the approved subject of
   the task's purpose.

## 2. Egress

1. No `web_fetch`, by anyone, for anything.
2. No message may carry identified records. This covers every destination:
   other agents, project members, and external addresses. Agent-to-agent
   messages count as outbound.

## 3. Cost caps

1. A call may not take the task's spend over the task cap. The task's spend
   so far plus the call's estimated cost may not exceed the task cap.
2. Likewise for the swarm's spend and the swarm cap.

Reaching a cap exactly is allowed.

## 4. Tool allowlist by role

Each role may use only the tools listed for it. A sub-agent never has more
tools than any agent above it in its chain: every role in the chain must
allow the tool.

| Tool | Allowed roles |
| --- | --- |
| `query_dataset` | pi, analyst, research_assistant |
| `read_document` | pi, analyst, research_assistant, external_collaborator |
| `web_fetch` | pi, analyst (still forbidden by rule 2.1) |
| `send_message` | pi, analyst, research_assistant, external_collaborator |
| `run_compute` | pi, analyst |
| `delegate` | pi, analyst |

## 5. Delegation depth

1. No agent may act at a depth greater than 3.
2. An agent may not delegate if the sub-agent would be at a depth greater
   than 3.

## Approved subjects by purpose

| Purpose | Approved subject |
| --- | --- |
| `pcsk9_trial_analysis` | PCSK9 inhibition and LDL-cholesterol lowering |
| `cv_outcomes` | major adverse cardiovascular events in patients on lipid-lowering therapy |
| `methods_dev` | statistical methods for longitudinal lipid measurements |
