import { beforeEach, describe, expect, test } from "vitest";
import { ApiClient, ApiError, FallbackStorage, NetworkError } from "./client.ts";

interface Call {
  method: string;
  path: string;
  auth: string | null;
  body: unknown;
}

type Handler = (call: Call) => Response | Error;

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** A fake tend that issues token-1, token-2, … and answers through handle. */
class FakeTend {
  calls: Call[] = [];
  tokens = 0;
  valid = new Set<string>();
  owner = true;
  handle: Handler = () => json(200, {});

  fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const headers = new Headers(init?.headers);
    const call: Call = {
      method: init?.method ?? "GET",
      path: String(input),
      auth: headers.get("Authorization"),
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    };
    this.calls.push(call);
    if (!this.owner) {
      return json(403, {
        error: { code: "forbidden", message: "only owners may use this service" },
      });
    }
    if (call.method === "POST" && call.path === "/api/v1/session") {
      const token = `token-${++this.tokens}`;
      this.valid.add(token);
      return json(200, {
        login: "owner@example",
        client_id: "c",
        scope: "owner",
        expires_at: "",
        token,
      });
    }
    if (!call.auth || !this.valid.has(call.auth.replace("Bearer ", ""))) {
      return json(401, { error: { code: "unauthenticated", message: "no valid token" } });
    }
    const out = this.handle(call);
    if (out instanceof Error) {
      throw out;
    }
    return out;
  };
}

class MemoryStorage {
  items = new Map<string, string>();
  getItem = (k: string) => this.items.get(k) ?? null;
  setItem = (k: string, v: string) => void this.items.set(k, v);
  removeItem = (k: string) => void this.items.delete(k);
}

let tend: FakeTend;
let storage: MemoryStorage;
let client: ApiClient;

beforeEach(() => {
  tend = new FakeTend();
  storage = new MemoryStorage();
  client = new ApiClient({ fetch: tend.fetch, storage });
});

describe("ApiClient", () => {
  test("signs in on first use and keeps the token and client ID", async () => {
    await client.request("GET", "/api/v1/snapshot");

    expect(tend.calls.map((c) => `${c.method} ${c.path} ${c.auth}`)).toEqual([
      "POST /api/v1/session null",
      "GET /api/v1/snapshot Bearer token-1",
    ]);
    const clientId = storage.getItem("tend.client_id");
    expect(clientId).toMatch(/^[A-Za-z0-9_-]{8,64}$/);
    expect(tend.calls[0]?.body).toEqual({ client_id: clientId, scope: "owner" });
    expect(storage.getItem("tend.token")).toBe("token-1");

    const reloaded = new ApiClient({ fetch: tend.fetch, storage });
    await reloaded.request("GET", "/api/v1/snapshot");
    expect(tend.calls.at(-1)?.auth).toBe("Bearer token-1");
    expect(tend.calls).toHaveLength(3);
  });

  test("signs in again when tend no longer accepts the token", async () => {
    await client.request("GET", "/api/v1/snapshot");
    tend.valid.clear();

    await client.request("GET", "/api/v1/snapshot");

    expect(tend.calls.slice(2).map((c) => `${c.method} ${c.path} ${c.auth}`)).toEqual([
      "GET /api/v1/snapshot Bearer token-1",
      "POST /api/v1/session null",
      "GET /api/v1/snapshot Bearer token-2",
    ]);
    expect(storage.getItem("tend.token")).toBe("token-2");
  });

  test("concurrent requests share one sign-in", async () => {
    await Promise.all([
      client.request("GET", "/api/v1/snapshot"),
      client.request("GET", "/api/v1/tasks"),
    ]);
    expect(tend.calls.filter((c) => c.path === "/api/v1/session")).toHaveLength(1);
  });

  test("a device that is not an owner's says so", async () => {
    tend.owner = false;
    const err = await client.connect().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe("forbidden");
    expect(client.connection.get()).toEqual({
      status: "not-owner",
      reason: "only owners may use this service",
    });
  });

  test("connect reports the owner", async () => {
    tend.handle = (c) =>
      c.path === "/api/v1/session"
        ? json(200, { login: "owner@example", client_id: "c", scope: "owner", expires_at: "" })
        : json(404, {});
    expect(client.connection.get()).toEqual({ status: "connecting" });
    await client.connect();
    expect(client.connection.get()).toEqual({ status: "online", login: "owner@example" });
  });

  test("no answer is a network error and the link is offline", async () => {
    await client.request("GET", "/api/v1/snapshot");
    tend.handle = () => new TypeError("Failed to fetch");
    await expect(client.request("GET", "/api/v1/snapshot")).rejects.toBeInstanceOf(NetworkError);
    expect(client.connection.get().status).toBe("offline");

    tend.handle = () => json(200, {});
    await client.request("GET", "/api/v1/snapshot");
    expect(client.connection.get()).toEqual({ status: "online", login: "owner@example" });
  });

  test("refusals carry the protocol error", async () => {
    tend.handle = () =>
      json(400, { error: { code: "invalid_request", message: "operation is malformed" } });
    const err = await client
      .submit({ request_id: "req_1", operation: "x", target: { type: "conversation" } })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      status: 400,
      code: "invalid_request",
      message: "operation is malformed",
    });
  });

  test("lookup returns null for a request tend never recorded", async () => {
    tend.handle = (c) =>
      c.path.endsWith("/req_known")
        ? json(200, { request_id: "req_known", operation: "o", status: "accepted" })
        : json(404, { error: { code: "not_found", message: "no such request" } });
    expect(await client.lookup("req_missing")).toBeNull();
    expect(await client.lookup("req_known")).toMatchObject({ status: "accepted" });
    expect(tend.calls.at(-1)?.path).toBe("/api/v1/requests/req_known");
  });
});

describe("blocked site storage", () => {
  // What localStorage does when the browser blocks site data.
  const blocked = (): Storage => {
    throw new DOMException("The operation is insecure.", "SecurityError");
  };

  test("FallbackStorage keeps values in memory", () => {
    const s = new FallbackStorage(blocked);
    expect(s.getItem("k")).toBeNull();
    s.setItem("k", "v");
    expect(s.getItem("k")).toBe("v");
    s.removeItem("k");
    expect(s.getItem("k")).toBeNull();
  });

  test("FallbackStorage uses working storage", () => {
    const backing = new MemoryStorage();
    const s = new FallbackStorage(() => backing);
    s.setItem("k", "v");
    expect(backing.getItem("k")).toBe("v");
    backing.setItem("other", "w");
    expect(s.getItem("other")).toBe("w");
  });

  test("the client still signs in and keeps its token for the page", async () => {
    const c = new ApiClient({ fetch: tend.fetch, storage: new FallbackStorage(blocked) });
    await c.request("GET", "/api/v1/snapshot");
    await c.request("GET", "/api/v1/snapshot");
    expect(tend.calls.map((x) => `${x.method} ${x.path} ${x.auth}`)).toEqual([
      "POST /api/v1/session null",
      "GET /api/v1/snapshot Bearer token-1",
      "GET /api/v1/snapshot Bearer token-1",
    ]);
  });

  test("the default storage survives a localStorage that throws", async () => {
    const saved = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
    Object.defineProperty(globalThis, "localStorage", { get: blocked, configurable: true });
    try {
      const c = new ApiClient({ fetch: tend.fetch });
      expect(c.clientId).toBe(c.clientId);
      await c.request("GET", "/api/v1/snapshot");
      expect(c.connection.get().status).toBe("online");
    } finally {
      if (saved) {
        Object.defineProperty(globalThis, "localStorage", saved);
      } else {
        delete (globalThis as { localStorage?: Storage }).localStorage;
      }
    }
  });
});
