import { expect, test, vi } from "vitest";
import type { TokenUsageView } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { UsageData } from "./data.ts";
const value = { revision: 1 } as TokenUsageView;
const settle = async () => {
  for (let i = 0; i < 10; i++) await Promise.resolve();
};

test("identical queries share a read and total history never polls", async () => {
  vi.useFakeTimers();
  try {
    const get = vi.fn(async () => value);
    const data = new UsageData({ get } as Reader);
    const a = data.watch("window=total&subject=run", () => {});
    const b = data.watch("subject=run&window=total", () => {});
    await settle();
    expect(get).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(get).toHaveBeenCalledTimes(1);
    data.refresh();
    await settle();
    expect(get).toHaveBeenCalledTimes(2);
    a();
    b();
    data.stop();
    expect(vi.getTimerCount()).toBe(0);
  } finally {
    vi.useRealTimers();
  }
});

test("hidden pages retain data and cancel failure retries until visible", async () => {
  vi.useFakeTimers();
  try {
    const get = vi
      .fn()
      .mockResolvedValueOnce(value)
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue(value);
    const data = new UsageData({ get } as Reader);
    let last: TokenUsageView | null = null;
    const release = data.watch("window=total", (s) => {
      last = s.value;
    });
    await settle();
    data.refresh();
    await settle();
    data.setVisible(false);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(get).toHaveBeenCalledTimes(2);
    expect(last).toBe(value);
    data.refresh();
    expect(get).toHaveBeenCalledTimes(2);
    data.setVisible(true);
    await settle();
    expect(get).toHaveBeenCalledTimes(3);
    release();
    data.stop();
  } finally {
    vi.useRealTimers();
  }
});

test("one minute scheduler refreshes visible dynamic windows only", async () => {
  vi.useFakeTimers();
  try {
    const get = vi.fn(async () => value);
    const data = new UsageData({ get } as Reader);
    const a = data.watch("window=24h", () => {});
    const b = data.watch("window=day&offset=1", () => {});
    await settle();
    expect(get).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(get).toHaveBeenCalledTimes(3);
    data.setVisible(false);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(get).toHaveBeenCalledTimes(3);
    data.setVisible(true);
    await settle();
    expect(get).toHaveBeenCalledTimes(5);
    a();
    b();
    data.stop();
  } finally {
    vi.useRealTimers();
  }
});

test("releasing or hiding an in-flight query cancels its signal", async () => {
  const signals: AbortSignal[] = [];
  const get = <T>(_path: string, signal?: AbortSignal) => {
    signals.push(signal!);
    return new Promise<T>(() => {});
  };
  const data = new UsageData({ get });
  const release = data.watch("window=total", () => {});
  data.setVisible(false);
  expect(signals[0]!.aborted).toBe(true);
  data.setVisible(true);
  release();
  expect(signals[1]!.aborted).toBe(true);
  data.stop();
});
