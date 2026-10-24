Self-hosted GPU cost, corpus `f5fdd0309581`. Shard wall time on the instance that ran it, at the us-east-1 on-demand list price of the smallest instance with that GPU (A10G g5.xlarge $1.006/h, L4 g6.xlarge $0.8048/h, L40S g6e.xlarge $1.861/h, T4 g4dn.xlarge $0.526/h). "with setup" spreads each instance's whole job time (weights download, server start) over its shards. Spot runs cost less; instance boot before the job is not counted. B0 calls no model; its shards only happened to run on a GPU instance.

| arm | decisions | GPU (shards) | GPU-hours | $ per 10k decisions | $ per 10k, with setup |
|---|---|---|---|---|---|
| a-d3 | 1000000 | L40S (200) | 160.5 | $2.99 | $3.17 |
| a-local | 1000000 | A10G (74), L4 (125), T4 (1) | 56.3 | $0.49 | $0.51 |
| a-strands | 1000000 | A10G (24), L4 (176) | 81.6 | $0.68 | $0.70 |
| b-4b | 1000000 | A10G (30), L4 (10) | 11.2 | $0.11 | $0.11 |
| b-strands | 1000000 | A10G (20), L4 (20) | 8.3 | $0.08 | $0.08 |
| b0 | 1000000 | A10G (1) | 0.1 | $0.00 | $0.00 |

Not priced here (no instance job log; hosted arms are priced by their API spend in the report): a-hosted (50 shards), b-hosted (3 shards), b-safeguard (50 shards).

Duplicated runs: 1, 0.4 run-hours (fleet relaunches of a range another complete run covers); the later run is the one scored and priced.
