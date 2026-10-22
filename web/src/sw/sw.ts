/// <reference lib="webworker" />
// The service worker: makes the app installable and lets its shell open
// without a network. The build replaces the manifest placeholder below
// with the files of the build (vite.config.ts), so each build has its own
// cache and an old one is dropped once the new worker takes over.
import { cachePrefix, route, staleCaches, type ShellManifest } from "./policy.ts";

declare const self: ServiceWorkerGlobalScope;

const manifest = JSON.parse("__TEND_SHELL_MANIFEST__") as ShellManifest;
const cacheName = cachePrefix + manifest.version;
const shell = new Set(manifest.files.map((f) => `/${f}`));

self.addEventListener("install", (event) => {
  event.waitUntil(
    (async () => {
      const cache = await caches.open(cacheName);
      // reload: skip the HTTP cache so the shell matches this build.
      await cache.addAll([...shell].map((path) => new Request(path, { cache: "reload" })));
      await self.skipWaiting();
    })(),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      for (const name of staleCaches(await caches.keys(), manifest.version)) {
        await caches.delete(name);
      }
      await self.clients.claim();
    })(),
  );
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  switch (route(req, self.location.origin, shell)) {
    case "network":
      return;
    case "page":
      event.respondWith(
        fetch(req).catch(async () => {
          const cached = await caches.match("/index.html", { cacheName });
          return cached ?? Response.error();
        }),
      );
      return;
    case "shell":
      event.respondWith((async () => (await caches.match(req, { cacheName })) ?? fetch(req))());
      return;
  }
});
