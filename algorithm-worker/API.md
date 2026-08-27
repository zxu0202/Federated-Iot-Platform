# AlgorithmCore and Worker API — M1 1.0.0-m1

## Scope and lifecycle

This M1 API owns preprocessing, feature construction, the generic Agent
runtime, local/global prediction, aggregation, fusion, alarms, and result
artifacts. A caller creates an `AlgorithmCore` with an immutable
`PreprocessingConfig`, calls it for one task, then releases its references.
There is no module-level mutable task state, model cache, random stream, or
fixed Agent service.

`WorkerRunner` receives one leased `worker.task.v1` envelope and a narrow
PostgreSQL `WorkerRepository`. It validates the envelope and input hash before
accessing a path. A `DATASET_PREFLIGHT` job fully executes and commits a
versioned `preflight_summary.json`. A `SIMULATION` job constructs validated
generic contexts, runs local kNN GP, public-anchor gPoE, leave-one-out global
surrogates, online fusion and alarms, atomically publishes all required
artifacts, then invokes the controlled simulation terminal commit function.

## Preprocessing API

```python
core = AlgorithmCore(PreprocessingConfig())
dataset = core.preprocess_csv(source_path, cancellation=context)
summary = core.preflight_summary(dataset)
core.write_preflight_summary(summary, destination)
```

Input is a read-only UTF-8 CSV with exactly these ordered columns:
`Time_base,dzdl_1,dzdl_2,dzdl_3,dzdl_4,zl,sd`.

Output `PreprocessedDataset` contains the SHA-256, counts, explicit filter
paths, time statistics, and immutable `RunningRow` values. The preprocessing
path is shared; preflight and simulation cannot select a second implementation.
The M1 default explicitly reports `median_filter_zero_padded_v1` and
`zero_phase_fir_v1`. The implementation uses the frozen moving-average
FIR coefficients, odd reflection, and steady-state forward/backward initial
conditions. Complete G1 numerical equivalence is verified against frozen
golden checkpoints; `filter_fir_v1` remains an explicit fallback mode and is
never selected silently.

## Feature API

```python
features = build_transition_dataset(agent_running_rows, n_lag=8)
split = chronological_split(len(features.values))
scaler = training_standardization(train_feature_rows)
```

`features.values` has logical shape `[N, 4*nLag+32]`, therefore `[N,64]` for
the frozen default. Feature order is returned by `feature_names(n_lag)` and is
never inferred from a dictionary. Standardization stores only training mean and
sample standard deviation; zero-scale dimensions use `1`.

## Agent runtime and aggregation boundary

`AgentContext` owns partition data, parameters, feature split,
standardization, models, residual/error windows, fusion state, random stream,
and output namespace. `AgentExecutor.execute(contexts, action, stage=...)`
iterates the provided collection without Agent-number branches.

`validate_s1_contexts()` is called at the S1 task boundary and requires exactly
`1/EARLY`, `2/MIDDLE`, and `3/LATE`. `AggregationPort` can receive only
immutable `AgentContribution` values; it cannot receive `AgentContext` or its
mutable internals.

## Errors, cancellation, and threading

The public worker error codes are `INPUT_INVALID`, `INSUFFICIENT_SAMPLES`,
`NUMERICAL_FAILURE`, `RESOURCE_LIMIT`, `CANCELLED`,
`ARTIFACT_WRITE_FAILED`, and `INTERNAL_ERROR`. Contract parsing uses
`WORKER_CONTRACT_MISMATCH`.

All long loops accept an attempt-scoped read-only cancellation context and
check it before their next bounded unit. The M1 runtime is single-process and
single-task. Algorithm-core functions do not create threads or issue database
calls. The repository cancellation adapter may run one task-scoped lease
renewal thread outside the algorithm core; it is stopped and joined before the
process can return to polling.
