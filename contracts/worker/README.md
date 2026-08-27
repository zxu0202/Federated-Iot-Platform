# Worker Contract Fixtures

`worker.task.v1.schema.json` and `artifact.manifest.v1.schema.json` are Draft
2020-12 structural contracts. They close every contract-controlled object and
separate `SIMULATION` from `DATASET_PREFLIGHT` with `oneOf` branches.

The Worker additionally applies executable semantic validation before it opens
an input path or publishes artifacts:

- `parse_worker_task()` enforces controlled storage layouts, S1's single
  complete task with exactly `1/EARLY`, `2/MIDDLE`, and `3/LATE`, immutable
  digests, and no cross-branch fields.
- `validate_artifact_manifest()` enforces unique safe logical paths, every
  non-self required v0.3 artifact, and, when passed the committed root, the
  actual regular-file size and SHA-256 values.

`fixtures/` contains one valid preflight task, one valid simulation task, one
valid artifact manifest envelope, and mutation-based negative cases. The
negative cases are exercised by `algorithm-worker/tests/test_contract_validation.py`.
They are synthetic offline contract fixtures only, not release-image evidence:
their IDs, digests, lease-token marker, and repository-relative paths do not
identify a deployed Worker, dataset, artifact, host, credential, or secret.
