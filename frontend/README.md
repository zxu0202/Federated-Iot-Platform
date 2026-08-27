# Frontend SPA

Version: `1.0.0-m1`

This directory contains the offline TypeScript single-page application for the S1 federated IoT simulation platform. The production bundle is served with the Web/API application and consumes the frozen REST/SSE transport at `/api/v1`.

## Architecture

- `src/main.ts` starts the application with `LivePlatformApi`.
- `src/api/` contains the API boundary and transport DTO helpers.
- `src/state/` owns simulation, SSE, readiness, and dataset-preflight polling state.
- `src/ui.ts` renders views, binds interactions, and projects immutable run snapshots and result resources into the DOM.
- `src/i18n.ts` contains the English and `zh-CN` presentation resources.
- `src/testing/` and `tests/` contain contract-only fixtures, mock adapters, and deterministic checks. They are excluded from production bundles.
- `public/` contains the static shell and design tokens. `scripts/build.mjs` compiles the selected production or mock entry point.

## M1 product surface

The SPA provides the following product pages and interactions using authoritative backend data:

- **Workspace**: frozen task identity and parameter snapshot, Agent 1/2/3 result selection, chart zoom/pan/keyboard inspection, synchronized diagnostics, alarms, traceability, result-file manifest, and an SSE event drawer.
- **Data**: local CSV intake, strict seven-column validation, upload progress, Worker preflight states, localized diagnostics, and preprocessing statistics.
- **Parameters**: immutable Reference profiles, editable Custom drafts for server-declared paths, Agent-specific sparse overrides, validation, save-as-new-version, rename, and restore-default-draft actions.
- **Queue**: one active slot, up to ten waiting tasks, authoritative cancellation capability, stage timeline, and incremental REST/SSE correction.
- **History and Replay**: server-filtered terminal-task history, frozen run snapshots, artifact-gated replay, point navigation, and backend-owned export links.

The UI never calculates algorithm results, changes backend state semantics, or substitutes current drafts for a run snapshot.

## Language, state, and accessibility

English (`en`) is the initial language on every application load. A user may switch to `zh-CN` for the current page session; the switch updates visible text and accessibility names without changing API/SSE fields, task state, drafts, selections, or chart viewports.

REST detail is authoritative. SSE applies only newer events to the selected run, while reconnect and terminal corrections refresh the same run. Incremental updates preserve focus, scroll position, filters, selected Agent, unsaved Custom drafts, and chart zoom/pan/series settings. Frozen run data, result rows, alarms, and result files are always gated by `run_id` before display.

Controls, dialogs, table rows, chart keyboard navigation, hash copy controls, status/error live regions, and localized dynamic labels are keyboard accessible. Long hashes are visually abbreviated within their value column while retaining a full accessible and copyable value.

## Responsive design

The stylesheet uses the prototype's 1x design tokens and local system font stack.

- **1024px**: Data and Workspace stack into a single reading column; the frozen parameter rail remains visible before Workspace content; diagnostics stack; component-local scrolling remains available for tables and alarms.
- **1366px to 1920px**: desktop Workspace rail, main content, and Data two-column layout are retained.
- **2200px and above**: the 2K token scale increases typography and controls together.
- **3400px and above**: the 4K token scale increases layout, typography, and chart dimensions together.

## Offline build and verification

Use the frozen Node `14.18.0` / npm `6.14.15` toolchain. `typescript@4.9.5` is the only build dependency. The local npm cache is an ignored machine-local build input; it is never uploaded or published.

```powershell
npm ci --offline --ignore-scripts --cache ./.npm-cache
npm run typecheck
npm run check:i18n
npm run test
npm run build
```

`npm run verify` performs the same complete sequence. It writes the production live-API bundle to `dist/`. `npm run build:mock` produces a contract-mock preview only and is not a production artifact.

The production API base is read from `data-api-base` in `public/index.html` and defaults to `/api/v1`. No external fonts, CDN assets, telemetry endpoint, or translation service is required.

## M2 and later

M2 work is limited to independently approved equivalence and integration gates. It must not change M1 API transport, Worker topology, frozen snapshot semantics, or the three-Agent S1 boundary without a new requirement and architecture decision.

## Repository hygiene

Public source, configuration, tests, lockfiles, and engineering documentation belong in this module. Generated bundles, dependencies, caches, coverage, temporary files, local environment values, editor-specific state, and credentials are excluded by the module `.gitignore`. This module is covered by the repository-level `LICENSE` (MIT); no module-local license file is required.
