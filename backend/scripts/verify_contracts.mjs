import { readdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";

const workspace = resolve(import.meta.dirname, "..", "..");
const apiRoot = join(workspace, "contracts", "api");

async function jsonFiles(root) {
  const output = [];
  for (const entry of await readdir(root, { withFileTypes: true })) {
    const full = join(root, entry.name);
    if (entry.isDirectory()) output.push(...await jsonFiles(full));
    else if (entry.name.endsWith(".json")) output.push(full);
  }
  return output;
}

const files = await jsonFiles(apiRoot);
for (const file of files) JSON.parse(await readFile(file, "utf8"));

const openapi = JSON.parse(await readFile(join(apiRoot, "openapi.v1.json"), "utf8"));
const requiredPaths = [
  "/datasets",
  "/configuration/reference-profile",
  "/simulations",
  "/simulations/{run_id}/cancel",
  "/simulations/{run_id}/events",
  "/simulations/{run_id}/summary",
  "/simulations/{run_id}/replay",
  "/simulations/{run_id}/artifacts",
  "/health/live",
  "/health/ready"
];
for (const path of requiredPaths) {
  if (!openapi.paths[path]) throw new Error(`OpenAPI path is missing: ${path}`);
}
if (openapi.openapi !== "3.1.0") throw new Error("OpenAPI must remain on 3.1.0");

const admission = JSON.parse(await readFile(join(apiRoot, "fixtures", "admission-one-running-ten-waiting.v1.json"), "utf8"));
if (admission.initial_state.running !== 1 || admission.operations[0].expected.queue_position !== 10 || admission.operations[1].expected.error_code !== "QUEUE_FULL") {
  throw new Error("Admission fixture no longer encodes the frozen 1+10 boundary");
}
const preflight = JSON.parse(await readFile(join(apiRoot, "fixtures", "dataset-preflight.v1.json"), "utf8"));
if (preflight.worker_task.job_type !== "DATASET_PREFLIGHT" || preflight.worker_task.run_id !== null) {
  throw new Error("Preflight fixture has crossed the Worker contract boundary");
}
console.log(`Validated ${files.length} JSON contracts and fixtures.`);
