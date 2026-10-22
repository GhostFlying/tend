import { describe, expect, test } from "vitest";
import { newClientId, newRequestId } from "./ids.ts";

// protocol.ValidID: the prefix, then 26 Crockford digits, the first at most 7.
const requestId = /^req_[0-7][0-9A-HJKMNP-TV-Z]{25}$/;

describe("newRequestId", () => {
  test("matches the protocol's format", () => {
    for (let i = 0; i < 100; i++) {
      expect(newRequestId(Date.now())).toMatch(requestId);
    }
    const max = () => new Uint8Array(10).fill(255);
    expect(newRequestId(2 ** 48 - 1, max)).toBe("req_7ZZZZZZZZZZZZZZZZZZZZZZZZZ");
  });

  test("encodes the time first", () => {
    const zero = (n: number) => new Uint8Array(n);
    expect(newRequestId(0, zero)).toBe("req_00000000000000000000000000");
    expect(newRequestId(1, zero)).toBe("req_00000000010000000000000000");
    expect(newRequestId(1000, zero) < newRequestId(1001, zero)).toBe(true);
  });
});

test("newClientId matches the session request's pattern", () => {
  expect(newClientId()).toMatch(/^[A-Za-z0-9_-]{8,64}$/);
  expect(newClientId()).not.toBe(newClientId());
});
