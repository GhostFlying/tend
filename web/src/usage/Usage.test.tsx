import { renderToStaticMarkup } from "react-dom/server";
import { expect, test } from "vitest";
import { TokenLine, field } from "./Usage.tsx";

test("unknown is distinct from an observed zero and partial counts stay explicit", () => {
  expect(field(0, 0, 2)).toBe("未提供");
  expect(field(0, 2, 2)).toBe("0");
  expect(field(123, 1, 2)).toBe("123（部分）");
});
test("cache details stay inside input, in the owner-approved order", () => {
  const totals = {
    requests: 1,
    input: 100,
    input_known: 1,
    cache_read: 60,
    cache_read_known: 1,
    cache_write: 10,
    cache_write_known: 1,
    output: 20,
    output_known: 1,
    total: 120,
    total_known: 1,
    final: 1,
  };
  const html = renderToStaticMarkup(<TokenLine totals={totals} />);
  expect(html.indexOf("输入 token")).toBeLessThan(html.indexOf("缓存读取"));
  expect(html.indexOf("缓存读取")).toBeLessThan(html.indexOf("缓存写入"));
  expect(html.indexOf("缓存写入")).toBeLessThan(html.indexOf("输出 token"));
  expect(
    renderToStaticMarkup(<TokenLine totals={{ ...totals, cache_write_known: 0 }} />),
  ).not.toContain("缓存写入");
});
