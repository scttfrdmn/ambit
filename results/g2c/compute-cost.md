Self-hosted GPU cost, corpus `1333d39b1cb9`. Shard wall time on the instance that ran it, at the us-east-1 on-demand list price of the smallest instance with that GPU (A10G g5.xlarge $1.006/h, L4 g6.xlarge $0.8048/h, L40S g6e.xlarge $1.861/h, T4 g4dn.xlarge $0.526/h). "with setup" spreads each instance's whole job time (weights download, server start) over its shards. Spot runs cost less; instance boot before the job is not counted. B0 calls no model; its shards only happened to run on a GPU instance.

| arm | decisions | GPU (shards) | GPU-hours | $ per 10k decisions | $ per 10k, with setup |
|---|---|---|---|---|---|
| a-d3 | 100000 | L40S (20) | 15.9 | $2.97 | $3.10 |
| a-local | 100000 | A10G (18), L4 (2) | 5.3 | $0.52 | $0.55 |
| a-strands | 100000 | A10G (17), L4 (3) | 8.0 | $0.78 | $0.81 |
| b-4b | 100000 | A10G (4) | 1.1 | $0.11 | $0.12 |
| b-strands | 100000 | A10G (2), L4 (2) | 0.8 | $0.08 | $0.09 |
| b0 | 100000 | A10G (1) | 0.0 | $0.00 | $0.00 |

Not priced here (no instance job log; hosted arms are priced by their API spend in the report): a-hosted (5 shards), a-safeguard (5 shards), b-hosted (5 shards), b-safeguard (5 shards).
