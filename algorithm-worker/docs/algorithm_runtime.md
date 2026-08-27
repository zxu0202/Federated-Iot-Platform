# M1 Algorithm Runtime Notes

## Execution boundary

`federated_iot_worker.simulation` implements the reference simulation inside
one Worker process. It receives the generic `AgentContext[]` created from a
single immutable `worker.task.v1` envelope and does not read a profile store,
use a fixed Agent service, or keep cross-task mutable state. The only
cross-Agent exchange is the explicit `FederatedAggregator` implementation of
the `AggregationPort` boundary.

The runtime executes the following reference order:

1. Agent-effective preprocessing, contiguous partitioning, feature creation,
   chronological split, and training-only standardization.
2. Local deterministic kNN squared-exponential GP prediction with the
   documented ill-conditioning/range fallback, trend blend, and per-sequence
   rate limiting.
3. Seeded base/transition/boundary centers, common public anchors, calibrated
   support-weighted gPoE, and one leave-one-out heteroscedastic surrogate per
   receiving Agent.
4. Calibration and online local/global fusion. Every test point fixes its
   prediction, interval, alarm and weight before its target updates bounded
   score or error windows.
5. Required CSV/JSON artifacts, a self-excluded `artifact.manifest.v1`, one
   same-filesystem directory rename, then `worker_commit_simulation`.

The Worker emits only database-approved running stages:
`PREPROCESSING`, `LOCAL_TRAINING`, `ANCHOR_AGGREGATING`,
`GLOBAL_DISTILLING`, `CALIBRATING`, and `TESTING`. The repository function
owns the final `GENERATING_ARTIFACTS -> COMPLETED` state transition.

## Reference checkpoint

`tests/test_reference_oracle.py` reads the immutable reference CSV and
`reference_checkpoint.v1.json`. It confirms the preprocessing row counts and
the first Agent 1 test-point local GP result after transition blend:

- original running index: `14059`;
- time: `2026-07-04 02:01:51.113`;
- local prediction: `99.368927131711` within `1e-4`.

The test never modifies reference assets and is evidence for only the covered
checkpoint, not a claim of complete-chain equivalence.

## Current measured performance evidence

On the current Windows CPython 3.10 development environment, the frozen
reference CSV preprocessing path completed in 11.605 seconds and produced
49,618 running rows. The pure-Python local GP implementation took 0.427295
seconds for one 100-neighbor, 64-feature Agent 1 test prediction and 2.716355
seconds for a four-point batch (0.679089 seconds per point). The result
matched the reference local standard deviation `0.1811878870299899` and the
blended prediction checkpoint above.

At the measured four-point batch cost, the approximately 14,880 reference
local calibration/test predictions alone project to about 168 minutes, before
anchors or global-surrogate predictions, and therefore exceed the S1
30-minute P1 target.
The approved local wheelhouse now contains the reviewed
`numpy==2.1.3` Linux/amd64 CPython 3.12 wheel with SHA-256
`2312b2aa89e1f43ecea6da6ea9a810d06aae08321609d8dc0d0eda6d946a541b`.
When NumPy is available, the Worker vectorizes kNN distance ranking, local
kernel/Cholesky solves, center selection, support distances, and global
surrogate Cholesky/prediction blocks. The pure-Python code remains an oracle
fallback only for the incompatible host environment. A conditional test checks
the NumPy local and global GP plus anchor-selection paths against that oracle
in the target runtime. The full reference benchmark and checkpoint report must
be generated in the approved CPython 3.12 Worker image; this module does not
download packages or build an image.

## Task Lease Maintenance

For a claimed PostgreSQL task, `WorkerRunner` starts one task-scoped lease
renewal context before preprocessing or numerical work begins. The context
invokes only `worker_heartbeat_for_worker` at the configured interval, under
the repository connection lock, so a CPU-intensive numerical section cannot
starve the 60-second task lease. It stops and joins before the Worker can
return to its poll loop. A failed renewal is raised at the next bounded
cancellation check; a renewal thread that cannot stop fails the Worker rather
than permitting another claim. Repository diagnostics identify the controlled
function name only and never log lease tokens, parameters, or connection data.
