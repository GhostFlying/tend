// The owner API client (D49, D70). The device's tailnet identity is the
// credential: the client asks POST /api/v1/session for an owner token when
// it has none or the server stops accepting it, so there is no password.
// A device that is not an owner's gets 403 and stays in that state.
import { Store, type Readable } from "../state/store.ts";
import type {
  APIResponse,
  Code,
  Envelope,
  Error as ErrorBody,
  RequestView,
  SessionRequest,
  SessionView,
} from "./generated/api.ts";
import { cryptoRandom, newClientId, type RandomBytes } from "./ids.ts";

/** What the client knows about its link to tend. */
export type Connection =
  | { status: "connecting" }
  | { status: "online"; login: string }
  | { status: "offline"; reason: string }
  | { status: "not-owner"; reason: string };

/** A request that reached tend and was refused. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: Code | undefined;

  constructor(status: number, body: ErrorBody | undefined) {
    super(body?.message ?? `HTTP ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = body?.code;
  }
}

/** A request with no answer: tend may or may not have received it. */
export class NetworkError extends Error {
  constructor(cause: unknown) {
    super("tend did not answer", { cause });
    this.name = "NetworkError";
  }
}

export type KeyValueStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

/**
 * Storage that keeps working in memory when the browser's throws, as
 * localStorage does when site data is blocked. Values then last only as
 * long as the page: a reload signs in again under a new client ID.
 */
export class FallbackStorage implements KeyValueStorage {
  private readonly backing: () => KeyValueStorage;
  private readonly memory = new Map<string, string>();

  /** backing is read on every call, so merely naming it cannot throw early. */
  constructor(backing: () => KeyValueStorage) {
    this.backing = backing;
  }

  getItem(key: string): string | null {
    try {
      const v = this.backing().getItem(key);
      if (v !== null) {
        return v;
      }
    } catch {
      // Fall back to memory.
    }
    return this.memory.get(key) ?? null;
  }

  setItem(key: string, value: string): void {
    this.memory.set(key, value);
    try {
      this.backing().setItem(key, value);
    } catch {
      // Kept in memory only.
    }
  }

  removeItem(key: string): void {
    this.memory.delete(key);
    try {
      this.backing().removeItem(key);
    } catch {
      // Gone from memory, which is all there is.
    }
  }
}

export interface ClientOptions {
  fetch?: typeof fetch;
  /**
   * Keeps the client ID and token across reloads. Default localStorage,
   * falling back to memory where it is unavailable.
   */
  storage?: KeyValueStorage;
  random?: RandomBytes;
  /** Bounds each request. Default 20 seconds. */
  timeoutMs?: number;
}

const clientIdKey = "tend.client_id";
const tokenKey = "tend.token";

export class ApiClient {
  private readonly fetch: typeof fetch;
  private readonly storage: KeyValueStorage;
  private readonly random: RandomBytes;
  private readonly timeoutMs: number;
  private readonly state = new Store<Connection>({ status: "connecting" });
  private signingIn: Promise<string> | null = null;
  /** The owner's login, from the last session answer. */
  private login = "";

  constructor(options: ClientOptions = {}) {
    this.fetch = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.storage = options.storage ?? new FallbackStorage(() => globalThis.localStorage);
    this.random = options.random ?? cryptoRandom;
    this.timeoutMs = options.timeoutMs ?? 20_000;
  }

  get connection(): Readable<Connection> {
    return this.state;
  }

  /** The ID this browser identifies itself with, created on first use. */
  get clientId(): string {
    let id = this.storage.getItem(clientIdKey);
    if (!id) {
      id = newClientId(this.random);
      this.storage.setItem(clientIdKey, id);
    }
    return id;
  }

  /** Checks the session, signing in when there is no valid token. */
  async connect(): Promise<SessionView> {
    const view = await this.request<SessionView>("GET", "/api/v1/session");
    this.login = view.login;
    this.markOnline();
    return view;
  }

  /** Executes one owner write (POST /api/v1/ops). */
  async submit(envelope: Envelope): Promise<APIResponse> {
    return this.request<APIResponse>("POST", "/api/v1/ops", envelope);
  }

  /** Reads the record of an earlier write, or null if tend has none. */
  async lookup(requestId: string): Promise<RequestView | null> {
    try {
      return await this.request<RequestView>(
        "GET",
        `/api/v1/requests/${encodeURIComponent(requestId)}`,
      );
    } catch (err) {
      if (err instanceof ApiError && err.code === "not_found") {
        return null;
      }
      throw err;
    }
  }

  /** Reads an owner API view. */
  async get<T>(path: string, signal?: AbortSignal): Promise<T> {
    return this.request<T>("GET", path, undefined, signal);
  }

  /** Uploads original bytes without imposing a total transfer time limit. */
  async upload(
    requestId: string,
    file: Blob,
    name: string,
    signal: AbortSignal,
  ): Promise<APIResponse> {
    const query = new URLSearchParams({
      request_id: requestId,
      name,
      media_type: file.type || "application/octet-stream",
    });
    return this.answer<APIResponse>(
      await this.authorized("POST", `/api/v1/conversation/attachments?${query}`, file, signal),
    );
  }

  /** Reads an immutable delivery file through owner authorization. */
  async file(fileId: string, signal?: AbortSignal): Promise<Blob> {
    const bounded = signal
      ? AbortSignal.any([signal, AbortSignal.timeout(this.timeoutMs)])
      : AbortSignal.timeout(this.timeoutMs);
    const response = await this.authorized(
      "GET",
      `/api/v1/files/${encodeURIComponent(fileId)}`,
      undefined,
      bounded,
      "application/octet-stream",
    );
    if (!response.ok) await this.answer(response);
    try {
      const blob = await response.blob();
      this.markOnline();
      return blob;
    } catch (err) {
      if (!bounded.aborted) this.markOffline("文件下载未完成");
      throw new NetworkError(err);
    }
  }

  /** Sends an authenticated request, signing in again once on 401. */
  async request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    return this.answer<T>(
      await this.authorized(
        method,
        path,
        body,
        signal ? AbortSignal.any([signal, AbortSignal.timeout(this.timeoutMs)]) : undefined,
      ),
    );
  }

  /**
   * Opens a streaming GET, such as the event stream, and returns the
   * response once tend accepts it; the caller reads the body. Only the
   * caller's signal ends it: the stream has no time limit of its own.
   */
  async openStream(path: string, signal: AbortSignal): Promise<Response> {
    const res = await this.authorized("GET", path, undefined, signal, "text/event-stream");
    if (!res.ok) {
      // Throws the protocol error.
      await this.answer(res);
    }
    this.markOnline();
    return res;
  }

  /** Records that tend still answers, as bytes on an open stream show. */
  noteAlive(): void {
    if (this.state.get().status !== "not-owner") {
      this.markOnline();
    }
  }

  /** Records that the link is down, as a stream that went quiet shows. */
  markOffline(reason: string): void {
    if (this.state.get().status !== "not-owner") {
      this.state.set({ status: "offline", reason });
    }
  }

  private async authorized(
    method: string,
    path: string,
    body?: unknown,
    signal?: AbortSignal,
    accept?: string,
  ): Promise<Response> {
    let token = this.storage.getItem(tokenKey) ?? (await this.signIn());
    let res = await this.send(method, path, body, token, signal, accept);
    if (res.status === 401) {
      this.storage.removeItem(tokenKey);
      token = await this.signIn();
      res = await this.send(method, path, body, token, signal, accept);
    }
    return res;
  }

  /** Asks for an owner token; concurrent callers share one request. */
  private signIn(): Promise<string> {
    this.signingIn ??= (async () => {
      try {
        const req: SessionRequest = { client_id: this.clientId, scope: "owner" };
        const res = await this.send("POST", "/api/v1/session", req);
        const view = await this.answer<SessionView>(res);
        if (!view.token) {
          throw new ApiError(res.status, { code: "internal", message: "no token in the session" });
        }
        this.storage.setItem(tokenKey, view.token);
        this.login = view.login;
        this.markOnline();
        return view.token;
      } finally {
        this.signingIn = null;
      }
    })();
    return this.signingIn;
  }

  private async send(
    method: string,
    path: string,
    body: unknown,
    token?: string,
    signal?: AbortSignal,
    accept = "application/json",
  ): Promise<Response> {
    const headers: Record<string, string> = { Accept: accept };
    if (token) {
      headers.Authorization = `Bearer ${token}`;
    }
    if (body !== undefined) {
      headers["Content-Type"] =
        body instanceof Blob ? body.type || "application/octet-stream" : "application/json";
    }
    try {
      return await this.fetch(path, {
        method,
        headers,
        ...(body === undefined ? {} : { body: body instanceof Blob ? body : JSON.stringify(body) }),
        cache: "no-store",
        signal: signal ?? AbortSignal.timeout(this.timeoutMs),
      });
    } catch (err) {
      if (!signal?.aborted) {
        // A caller that gave up learns nothing about the link.
        this.markOffline("连不上 tend");
      }
      throw new NetworkError(err);
    }
  }

  /** Any answer proves the link works. */
  private markOnline(): void {
    const current = this.state.get();
    if (current.status !== "online" || current.login !== this.login) {
      this.state.set({ status: "online", login: this.login });
    }
  }

  private async answer<T>(res: Response): Promise<T> {
    let body: unknown;
    try {
      body = await res.json();
    } catch (err) {
      if (res.ok) {
        // A cut-off answer leaves the outcome unknown.
        this.state.set({ status: "offline", reason: "tend 的回答不完整" });
        throw new NetworkError(err);
      }
    }
    if (res.ok) {
      this.markOnline();
      return body as T;
    }
    const error = (body as APIResponse | undefined)?.error;
    if (res.status === 403) {
      this.state.set({ status: "not-owner", reason: error?.message ?? "这台设备不是 owner" });
    }
    throw new ApiError(res.status, error);
  }
}
