# Worker Lifecycle Contract v1

The S1 Worker accepts only `worker.task.v1` envelopes through the PostgreSQL
Worker Repository. It does not publish an HTTP endpoint and it does not open a
SQLite database file.

## Validation

Before accessing a dataset, the Worker validates the contract version, job
type, safe relative paths, source SHA-256, and output schema. A simulation
additionally requires one ordered generic collection containing exactly:
`1/EARLY`, `2/MIDDLE`, and `3/LATE`.

## Cancellation and lease ownership

The repository owns leases. Every status, event, summary, and terminal write is
scoped by `job_id`, `attempt_id`, and `lease_token`. The Worker checks the
persisted cancellation context before preprocessing, between preprocessing
signals, before every generic Agent execution, and before each later bounded
numeric batch. A cancellation produces `CANCELLED`; it never commits a complete
artifact manifest.

## Progress events

The Worker persists `worker.event.v1` messages through the repository. The
allowed algorithm stages are `PREPROCESSING`, `LOCAL_TRAINING`,
`ANCHOR_AGGREGATING`, `GLOBAL_DISTILLING`, `CALIBRATING`, and `TESTING`.
Diagnostics contain bounded counts, shapes, fallback counters, or hashes only;
they never contain raw rows, paths, models, or lease tokens.

## Preflight

`DATASET_PREFLIGHT` calls the same `AlgorithmCore.preprocess_csv()` used by a
simulation. It writes `preflight_summary.json` atomically, then calls the
repository's preflight commit operation. It does not construct Agent contexts,
train a model, or create simulation artifacts.

## Artifact commit

Simulation artifacts are written under an attempt-specific temporary directory.
Every file is closed and hashed before `artifact_manifest.json` is written; the
manifest excludes itself. The Worker performs exactly one same-volume directory
rename to `committed/`, then the repository performs the lease-checked database
terminal transaction. If either step fails, `COMPLETED` must not be published.

M1 executes the frozen full simulation path: local GP, aggregation, global
distillation, online fusion, result CSVs, and the lease-checked final commit.
M2+ work is limited to independently approved equivalence or integration
verification; it does not weaken this lifecycle or replace its immutable task
and artifact boundaries.
