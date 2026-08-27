# M1 Custom Parameter and Runtime Audit

## Scope and immutable input

This document records the Algorithm Worker's M1 handling of CUSTOM parameter
snapshots. It does not package an external source inventory, raw dataset,
historical output, or source-location evidence.

For a SIMULATION, the Worker reads only the claimed `worker.task.v1` envelope:
the parameter profile version/SHA-256, full `shared_parameters`, the three
frozen sparse Agent override objects, mapping, field standard, and runtime
snapshot. It has no profile-store read path, no HTTP configuration client, and
no hot-reload path. A later saved CUSTOM profile can therefore affect only a
new task envelope; it cannot alter an already parsed or running task.

`parameter_snapshot.shared_parameters` is a closed, complete frozen parameter
tree. It contains `feature_state`, `cleaning`, `split`, `local_gp`, `trend`,
`interval`, `anchors`, `support`, `global_surrogate`, `fusion`, and `alarms`.
It contains exactly 69 leaves: 67 editable leaves plus the fixed
`split.agent_count` and `global_surrogate.leave_one_out` leaves. The Worker
does not insert defaults for missing leaves. Unknown
groups/leaves, non-object overrides, wrong scalar types, NaN, and infinity
raise `ContractFailure` before algorithm work begins.

`split.agent_count` remains constrained to `3`. It is a stored structural
constant, not a CUSTOM scaling control: S1 still requires exactly
`1/EARLY`, `2/MIDDLE`, and `3/LATE` in one complete Worker task.

## Effective Agent parameters

For each Agent, the Worker recursively merges its sparse `parameters` object
onto the complete shared tree, validates the merged value, and deep-freezes it
inside that `AgentContext`. Every context owns a separate effective mapping,
runtime state, random stream, output namespace, feature matrix, and split.
There is no mutable task-global parameter map and no Agent-specific service or
copied algorithm branch.

| Frozen field | Effective M1 action |
| --- | --- |
| `feature_state.nLag` | Agent-specific transition-feature width and partition guard. |
| `feature_state.speed_threshold`, `feature_state.current_threshold` | Agent-effective preprocessing state classifier. |
| `cleaning.median_window`, `cleaning.mad_factor`, `cleaning.smoothing_window` | Agent-effective preprocessing repair and smoothing configuration. |
| `split.training_ratio`, `split.calibration_ratio`, and minimum sample counts | Agent-specific chronological split and viability checks. |
| `trend.gain` | Agent-specific transition-feature trend value. |
| `runtime.random_streams.base_center_seed_by_agent` | Agent-specific `AgentContext.random_seed` and independent random stream. |
| `local_gp` | Per-query deterministic kNN squared-exponential GP, adaptive neighbor count, regularization, conditioning fallback, and variance floor inputs. |
| remaining `trend` | Transition-score blend flags/limits and independent calibration/test rate-limit sequences. |
| `interval` | Initial and online residual windows, empirical quantiles, standard-deviation/variance floors, half-width limits, and rolling coverage diagnostics. |
| `anchors` | Agent-local base/transition/boundary centers, seeded MT19937 selection, public-anchor budget, and cluster iterations. A lower Agent public budget removes that Agent's support contribution beyond its budget; equal REFERENCE budgets preserve one common 300-anchor set. |
| `support` | Anchor gPoE support, downloaded-surrogate query support, and the online fusion support gate. |
| `global_surrogate` | Per-Agent leave-one-out heteroscedastic GP length scale, regularization, noise ratio, and bounded Cholesky retries. |
| `fusion` | Calibration alpha, bounded error windows, reliability, persistent expert selection, negative-transfer gates, and asymmetric smoothing. |
| `alarms` | Load persistence thresholds, optional absolute thresholds, and data-quality alarm classification. |

The task-level mapping, field-standard snapshot, profile version/SHA-256, and
other runtime streams remain immutable evidence. They are structurally
validated by `worker.task.v1`; the unimplemented stages do not consume them.

## Agent-specific preprocessing and local cache

The frozen preprocessing contract runs before partitioning. CUSTOM therefore needs
an explicit extension when Agent effective preprocessing fields differ. The
Worker uses one reusable `AlgorithmCore` implementation and processes the same
frozen CSV sequentially for each distinct effective `PreprocessingConfig`:

1. Build the complete merged configuration for Agents 1, 2, and 3.
2. Use the complete config tuple (contract version, thresholds, cleaning
   windows/factor, and fixed filter paths) as a task-local cache key.
3. Reuse a dataset only when that entire key is identical; otherwise run the
   same pipeline again for that Agent.
4. Partition, construct features, split, and construct that Agent's generic
   `AgentContext` from its own preprocessing result.

This yields one preprocessing call for REFERENCE or a CUSTOM task whose three
effective preprocessing configurations are equal, and at most three calls for
three different configurations. The cache is a local dictionary scoped to one
`WorkerRunner` task execution; it cannot survive, mutate, or contaminate a
different envelope. The Worker remains single-capacity and serial.

## DATASET_PREFLIGHT

`DATASET_PREFLIGHT` still forbids every simulation snapshot field. It executes
once with the reference `PreprocessingConfig` through the same `AlgorithmCore`
implementation and writes its normal summary. It does not parse, cache, or
inherit CUSTOM values. This preserves the frozen preflight contract.

## Verification

The Worker tests cover:

- three different sparse Agent overrides for cleaning/thresholds, feature
  width, split, trend, local-GP evidence, and runtime seeds;
- actual `AlgorithmCore` configuration selection and one-versus-three
  task-local preprocessing cache calls;
- independent Agent-effective mappings and feature/split outcomes;
- envelope isolation: later mutation/new-task parameters cannot change an
  already parsed task;
- stable rejection of unknown paths, wrong types, NaN, infinity, and missing
  complete shared trees;
- unchanged preflight behavior, atomic required-artifact publication, and
  repository commit only after the local directory rename;
- a read-only frozen oracle for the first Agent 1 test point,
  including preprocessing counts, local GP standard deviation, and blended
  local prediction.

## Backend integration contract

Backend must create the frozen envelope before the Worker claims it. To make
this executable, its reference/CUSTOM profile validation and task fixture must
emit the complete tree named above, including the fixed Agent count and
leave-one-out leaves. Trend blending, prediction rate limiting, and diagnostic
top-N selection are deterministic Worker behavior, not profile fields.
Existing empty profile records and fixture examples are not
accepted by this Worker; they must be migrated as new immutable profile
versions rather than modified in place. Backend must canonicalize the full
merged snapshot and its SHA-256 before enqueueing, and must never update it
after enqueueing. Agent overrides use the same nested paths as shared
parameters and must be retained verbatim in the frozen Agent snapshot.

No API contract, Backend source, reference asset, or Worker task schema was
modified by this Algorithm Worker change.
