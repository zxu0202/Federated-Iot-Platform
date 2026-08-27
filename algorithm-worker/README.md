# Algorithm Worker

Version: **1.0.0-m1**. This package is the M1 S1 Algorithm Worker: one generic
Worker process executes one leased task at a time and owns its attempt-local
numerical state. It has no SQLite adapter, fixed Agent service, cross-Worker
task split, profile-store read path, or HTTP API.

## Generic S1 execution model

`AlgorithmCore.preprocess_csv()` is the only preprocessing pipeline for both
`DATASET_PREFLIGHT` and `SIMULATION`. A SIMULATION builds an `AgentContext[]`
from the immutable `worker.task.v1` envelope. `AgentExecutor` applies each
stage to the supplied context collection without Agent-number branches, and
the explicit `AggregationPort` accepts immutable contributions only. The M1
topology is exactly one Worker with Agent `1/EARLY`, `2/MIDDLE`, and `3/LATE`.

The simulation lifecycle is:

1. Validate the frozen envelope and dataset hash before resolving a storage
   path.
2. Run reference preflight once, or run the shared preprocessing pipeline once
   per distinct Agent-effective preprocessing configuration.
3. Partition, construct features, split chronologically, standardize from
   training rows, then execute local GP, anchors, gPoE, LOO global surrogates,
   online fusion, intervals, alarms, metrics, and diagnostics.
4. Publish the required result set through `AtomicArtifactWriter`, with one
   same-filesystem directory rename, before the controlled terminal repository
   commit.

`WorkerRunner` emits bounded lifecycle events, checks cancellation during long
loops, and starts a task-scoped lease renewal context before numerical work.
Lease loss, cancellation, malformed input, or an artifact/repository failure
ends the current task; the service does not silently reuse that lease or claim
another task after an unsafe repository failure.

## Frozen parameter and recovery boundaries

SIMULATION consumes only the admitted `parameter_snapshot` in the claimed
envelope. Its complete shared tree has exactly 69 paths: 67 editable CUSTOM
paths plus fixed `split.agent_count=3` and
`global_surrogate.leave_one_out=true`. Sparse Agent 1/2/3 overrides merge into
separate immutable effective mappings, which are actually consumed by their
contexts. Unknown, missing, non-finite, or wrongly typed paths fail before
algorithm work. A later profile edit affects only a newly created envelope.

`DATASET_PREFLIGHT` has no parameter snapshot and always uses the reference
preflight contract. Determinism comes from the frozen task snapshot, explicit
random streams, ordered input processing, and task-local caches. Recovery and
retry consume a newly leased immutable envelope; they do not read a latest
profile or retain mutable state from a previous attempt.

## Result artifacts

Every successful SIMULATION writes a self-excluded `artifact.manifest.v1` and
the required eleven files: `run_manifest.json`,
`preprocessing_summary.json`, `agent_partition_summary.csv`,
`feature_schema.json`, `anchor_summary.json`, `metrics.csv`, three
`results_agent_<n>.csv` files, `alarms.csv`, and `diagnostics.json`. Result and
alarm CSV `Time` text remains the frozen source-local value. Repository payloads
carry safe result locators and the manifest `snapshot_sha256` exactly matches
the task parameter-snapshot hash.

## Offline build and test inputs

`requirements.lock` exactly pins the Linux/amd64 CPython 3.12 runtime:
`numpy==2.1.3`, `psycopg==3.3.4`, `psycopg-binary==3.3.4`, and
`typing-extensions==4.16.0`. The reviewed local `wheelhouse/` and its
`SHA256SUMS` file provide offline install inputs. Wheel binaries remain local
and ignored; the lock and checksum manifest remain auditable. Installation
uses `--no-index --require-hashes`; neither build nor runtime downloads
packages.

Run the offline test suite from this directory:

```powershell
python -m unittest discover -s tests -v
```

The module `.gitignore` keeps source, tests, contracts, locks, checksum
manifests, and engineering documentation while excluding environments, caches,
wheel binaries and package outputs, runtime attempts, datasets, generated
artifacts, logs, local configuration, credentials, and editor-specific state.

## M1 scope and M2+ work

M1 implements the frozen single-Worker, three-Agent PostgreSQL-backed
execution boundary and its full artifact/recovery contract. M2+ work is
independent equivalence and integration verification under renewed approval;
it must not add SQLite, Agent cardinality changes, cross-Worker sharding,
parameter paths, fixed Agent services, profile hot reload, or unpublished
dependencies without the relevant requirement and algorithm gates.
