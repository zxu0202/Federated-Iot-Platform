import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const fixtureRoot = resolve(import.meta.dirname, "..", "fixtures", "worker");
const fieldMaterial = JSON.parse(await readFile(resolve(fixtureRoot, "field-standard.snapshot-input.v1.json"), "utf8"));
const envelope = JSON.parse(await readFile(resolve(fixtureRoot, "worker-task-v1.simulation.json"), "utf8"));
const ociManifest = await readFile(resolve(fixtureRoot, "test-only-worker-image.oci-manifest.json"));
const ociDescriptor = JSON.parse(await readFile(resolve(fixtureRoot, "test-only-worker-image.oci-descriptor.json"), "utf8"));

function canonicalize(value) {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.keys(value).sort().map(key => [key, canonicalize(value[key])]));
  }
  return value;
}

function canonicalSHA256(value) {
  return createHash("sha256").update(JSON.stringify(canonicalize(value))).digest("hex");
}

const fieldHash = canonicalSHA256(fieldMaterial);
const imageDigest = `sha256:${createHash("sha256").update(ociManifest).digest("hex")}`;
if (envelope.field_standard_snapshot?.sha256 !== fieldHash) {
  throw new Error(`field_standard_snapshot.sha256 mismatch: expected ${fieldHash}`);
}
if (ociDescriptor.digest !== imageDigest || ociDescriptor.scope !== "contract-test-only" || ociDescriptor.release_candidate_image !== false) {
  throw new Error(`OCI descriptor mismatch: expected ${imageDigest}`);
}
if (envelope.runtime?.image_digest !== imageDigest) {
	throw new Error(`runtime.image_digest mismatch: expected ${imageDigest}`);
}
const expectedRandomStreams = {
  generator: "MT19937_TWISTER_COMPAT",
  seed_mapping_version: "reference-anchor-v1",
  base_center_seed_by_agent: { "1": 2027, "2": 2028, "3": 2029 },
  transition_center_seed_by_agent: { "1": 2047, "2": 2048, "3": 2049 },
  boundary_seed_by_agent: { "1": 2067, "2": 2068, "3": 2069 },
  public_anchor_seed: 2126,
};
if (envelope.run_mode !== "REFERENCE" || envelope.runtime?.master_seed !== 2026 || JSON.stringify(envelope.runtime?.random_streams) !== JSON.stringify(expectedRandomStreams)) {
	throw new Error("REFERENCE runtime random streams are not the frozen deterministic mapping");
}
const observedAgents = envelope.parameter_snapshot?.agents?.map(({ agent, segment }) => ({ agent, segment }));
const expectedAgents = [{ agent: 1, segment: "EARLY" }, { agent: 2, segment: "MIDDLE" }, { agent: 3, segment: "LATE" }];
if (JSON.stringify(observedAgents) !== JSON.stringify(expectedAgents)) {
	throw new Error("SIMULATION must contain exactly Agent 1/EARLY, 2/MIDDLE, and 3/LATE");
}
if (imageDigest.includes("fixture") || fieldHash.includes("fixture")) {
  throw new Error("fixture placeholders are forbidden");
}
console.log(JSON.stringify({ field_standard_sha256: fieldHash, test_only_oci_digest: imageDigest, scope: "contract-test-only; not an RC image digest" }));
