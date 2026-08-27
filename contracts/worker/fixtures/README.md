# Worker Contract Fixtures

These files are synthetic offline contract fixtures for Backend, Frontend, and
QA. They contain no dataset, generated result, host path, credential, key, or
password.

- `preflight-task.v1.json` is a valid `DATASET_PREFLIGHT` envelope with a
  synthetic dataset identifier, digest, and repository-relative path.
- `simulation-task.v1.json` is a valid `SIMULATION` envelope with synthetic
  immutable snapshot and image digests.
- `preflight-event.v1.json` is a representative preprocessing event. Its
  diagnostic summary marker does not identify an emitted result.

The `lease_token` value is the literal noncredential marker
`fixture-token-not-a-secret`. Every digest is a pattern-valid synthetic value,
not a digest for local data, a result, an image, or a deployed artifact.
