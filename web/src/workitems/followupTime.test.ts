import { expect, test } from "vitest";
import { localTime, utcTime } from "./followupTime.ts";
test("appointment converts the explicit zone rather than browser local time", () => {
  expect(utcTime("2026-10-27T09:30", "Asia/Shanghai")).toBe(Date.parse("2026-10-27T01:30:00Z"));
  expect(localTime(Date.parse("2026-10-27T01:30:00Z"), "Asia/Shanghai")).toBe("2026-10-27T09:30");
  expect(utcTime("2026-10-27T09:30", "Asia/Kathmandu")).toBe(Date.parse("2026-10-27T03:45:00Z"));
});
test("ambiguous, nonexistent and invalid wall-clock appointments require correction", () => {
  expect(() => utcTime("2026-03-08T02:30", "America/New_York")).toThrow("没有这个时间");
  expect(() => utcTime("2026-11-01T01:30", "America/New_York")).toThrow("重复");
  expect(() => utcTime("2026-02-30T12:00", "UTC")).toThrow();
  expect(() => utcTime("2026-10-27T09:30", "Local")).toThrow();
});
