import { LivePlatformApi } from "./api/live-api.js";
import { startApplication } from "./ui.js";

const root = document.querySelector<HTMLElement>("#app");
if (!root) throw new Error("Missing #app mount point.");
startApplication(root, new LivePlatformApi(root.dataset.apiBase ?? "/api/v1"));
