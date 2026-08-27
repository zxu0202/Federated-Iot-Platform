# API Contracts

This directory contains the public source of the versioned REST, SSE, and
Worker-boundary contracts for Federated IoT Platform `1.0.0-m1`.

## Contents

- `openapi.v1.json` is the OpenAPI 3.1 transport contract.
- `schemas/` contains standalone JSON Schema documents used at contract
  boundaries.
- `fixtures/` contains deterministic, synthetic contract examples for
  admission, cancellation, dataset preflight, idempotency, SSE reconnect, and
  Worker lease recovery.

All fixture identifiers use `fixture` or `example` names and are not runtime
records. Relative paths such as `datasets/<dataset_id>/source.csv` are contract
storage keys, not host filesystem paths. Protocol fields such as
`lease_token` remain present where the contract requires them; fixture values
are inert placeholders and must never be copied into a deployment.

## Validation

From the repository root, use Node.js 20.11 or later to validate JSON parsing
and the cross-fixture contract invariants without network access:

```powershell
node code/backend/scripts/verify_contracts.mjs
```

## Source-release boundary

Include the OpenAPI document, JSON Schemas, fixtures, and this README in a
source release. Exclude generated bundles, local overrides, credentials,
runtime logs, datasets, artifacts, and test evidence. This directory contains
no deployment secret material and is covered by the repository-level `LICENSE`
(MIT).
