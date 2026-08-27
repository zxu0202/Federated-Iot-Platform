/*
 * Offline build script for TypeScript sources that intentionally use the
 * JavaScript-compatible subset of TypeScript. It creates ESM browser assets
 * without downloading a bundler or a runtime dependency.
 */
import { copyFile, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = join(root, "dist");
const mock = process.argv.includes("--mock");
const packageMetadata = JSON.parse(await readFile(join(root, "package.json"), "utf8"));

await rm(dist, { recursive: true, force: true });
await mkdir(dist, { recursive: true });

async function copyDirectory(from, to) {
  await mkdir(to, { recursive: true });
  const entries = await readdir(from, { withFileTypes: true });
  await Promise.all(entries.map(entry => entry.isDirectory()
    ? copyDirectory(join(from, entry.name), join(to, entry.name))
    : copyFile(join(from, entry.name), join(to, entry.name))));
}

await copyDirectory(join(root, "public"), dist);

const compiler = process.platform === "win32" ? "tsc.cmd" : "tsc";
const compilation = spawnSync(compiler, ["--project", join(root, mock ? "tsconfig.mock.json" : "tsconfig.build.json")], { stdio: "inherit" });
if (compilation.status !== 0) throw new Error("TypeScript build failed.");
const indexPath = join(dist, "index.html");
const index = await readFile(indexPath, "utf8");
await writeFile(indexPath, index.replace("__ENTRY_MODULE__", mock ? "./mock.js" : "./main.js"), "utf8");
await writeFile(join(dist, "BUILD_INFO.json"), JSON.stringify({
  build_mode: mock ? "contract-mock" : "live-api",
  frontend_version: packageMetadata.version,
  api_contract_version: "0.4",
  generated_at: new Date().toISOString()
}, null, 2) + "\n", "utf8");
