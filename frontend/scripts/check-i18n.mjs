/* Validates that English and Simplified Chinese expose the same stable keys. */
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const text = await readFile(join(root, "src", "i18n.ts"), "utf8");
const keyMatches = [...text.matchAll(/"([a-zA-Z0-9_.]+)":\s*"/g)].map(match => match[1]);
const separators = keyMatches.reduce((all, key, index) => (key === "nav.workspace" ? [...all, index] : all), []);
if (separators.length !== 2) throw new Error("Expected two locale resource blocks.");
function additionalKeys(name) {
  const match = text.match(new RegExp(`const ${name} = \\{([\\s\\S]*?)\\n\\};`));
  if (!match) throw new Error(`Missing ${name}.`);
  return [...match[1].matchAll(/"([a-zA-Z0-9_.]+)":\s*"/g)].map(key => key[1]);
}
const english = new Set([...keyMatches.slice(separators[0], separators[1]), ...additionalKeys("additionalEnglishResources"), ...additionalKeys("additionalCreateEnglishResources"), ...additionalKeys("additionalDraftEnglishResources"), ...additionalKeys("currentUiEnglishResources"), ...additionalKeys("currentUiEnglishDataContractResources")]);
const chinese = new Set([...keyMatches.slice(separators[1]), ...additionalKeys("additionalChineseResources"), ...additionalKeys("additionalCreateChineseResources"), ...additionalKeys("additionalDraftChineseResources"), ...additionalKeys("currentUiChineseResources"), ...additionalKeys("currentUiChineseDataContractResources")]);
const missingChinese = [...english].filter(key => !chinese.has(key));
const missingEnglish = [...chinese].filter(key => !english.has(key));
if (missingChinese.length || missingEnglish.length) {
  throw new Error(`Locale key mismatch. Missing zh-CN: ${missingChinese.join(", ")}; missing en: ${missingEnglish.join(", ")}`);
}
console.log(`i18n keys verified: ${english.size}`);
