import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import {
  configureDeployment,
  clearOldDeploymentData,
  deploymentChangeTarget,
} from "./api/deployment.ts";
import { ApiClient, FallbackStorage } from "./api/client.ts";
import { App } from "./App.tsx";
import { startConnector } from "./app/connector.ts";
import { LiveData } from "./live/data.ts";
import { openOutbox } from "./queue/outbox.ts";
import { SendQueue } from "./queue/sendQueue.ts";
import "./styles/global.css";
import "./theme/theme.ts";

const client = new ApiClient();
let changed = false;
const channel =
  typeof BroadcastChannel === "undefined" ? null : new BroadcastChannel("tend-deployment");
async function deploymentChanged(next: string) {
  if (changed || next === client.deploymentId) return;
  changed = true;
  channel?.postMessage({ previous: client.deploymentId, deployment: next });
  await clearOldDeploymentData(client.deploymentId);
  location.reload();
}
window.addEventListener("tend:deployment-changed", (event) => {
  void deploymentChanged((event as CustomEvent<string>).detail);
});
if (channel)
  channel.onmessage = (event: MessageEvent<unknown>) => {
    const next = deploymentChangeTarget(event.data, client.deploymentId);
    if (next) void deploymentChanged(next);
  };

async function start() {
  if (!new FallbackStorage(() => localStorage).getItem("tend.deployment_id"))
    await clearOldDeploymentData("");
  // No durable queue or draft is opened until the instance is established.
  let verified = false;
  try {
    await client.connect();
    verified = true;
  } catch (error) {
    // Existing drafts stay in their original instance while offline. The API
    // checks that identity before reconnect can resume a queue.
    if (changed || !client.deploymentId) throw error;
  }
  configureDeployment(client.deploymentId);
  const live = new LiveData(client);

  // Without IndexedDB (some private modes) nothing can be written down
  // first, so nothing is sent; the composer says so.
  const queue = openOutbox()
    .then((outbox) => SendQueue.open({ outbox, sender: client }))
    .catch((err: unknown) => {
      console.error("tend: the outbox is unavailable", err);
      return null;
    });

  // The initial handshake completed before opening the persisted queue.
  if (verified) void queue.then((q) => q?.resume());

  // Each time the link to tend is up, send what the outbox holds.
  const link = startConnector(client, live, () => {
    void queue.then((q) => q?.resume());
  });

  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <App client={client} live={live} connection={link.connection} queue={queue} />
    </StrictMode>,
  );

  if (import.meta.env.PROD && "serviceWorker" in navigator) {
    const register = () => {
      navigator.serviceWorker.register("/sw.js").catch((err: unknown) => {
        // Plain http: the page works, it just cannot be installed.
        console.warn("tend: no service worker", err);
      });
    };
    if (document.readyState === "complete") register();
    else window.addEventListener("load", register, { once: true });
  }
}
void start().catch((error: unknown) => {
  if (!changed)
    document.getElementById("root")!.textContent =
      `暂时无法连接 Tend：${String(error)}。请刷新重试。`;
});
