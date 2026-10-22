import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ApiClient } from "./api/client.ts";
import { App } from "./App.tsx";
import { startConnector } from "./app/connector.ts";
import { openOutbox } from "./queue/outbox.ts";
import { SendQueue } from "./queue/sendQueue.ts";
import "./styles/global.css";
import "./theme/theme.ts";

const client = new ApiClient();

// Without IndexedDB (some private modes) nothing can be written down
// first, so nothing is sent; the composer says so.
const queue = openOutbox()
  .then((outbox) => SendQueue.open({ outbox, sender: client }))
  .catch((err: unknown) => {
    console.error("tend: the outbox is unavailable", err);
    return null;
  });

// Each time the link to tend is up, send what the outbox holds.
startConnector(client, () => {
  void queue.then((q) => q?.resume());
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App client={client} queue={queue} />
  </StrictMode>,
);

if (import.meta.env.PROD && "serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch((err: unknown) => {
      // Plain http: the page works, it just cannot be installed.
      console.warn("tend: no service worker", err);
    });
  });
}
