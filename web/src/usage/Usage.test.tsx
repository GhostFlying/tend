import { renderToStaticMarkup } from "react-dom/server";
import { expect, test } from "vitest";
import { TokenLine, UsageSummary, field } from "./Usage.tsx";

test("unknown is distinct from an observed zero and partial counts stay explicit", () => {
  expect(field(0, 0, 2)).toBe("未提供");
  expect(field(0, 2, 2)).toBe("0");
  expect(field(123, 1, 2)).toBe("123（部分，1/2 个请求）");
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

test("summary distinguishes no requests, missing total, known zero and partial coverage", () => {
  const t = {
    requests: 1,
    input: 120,
    input_known: 1,
    cache_read: 100,
    cache_read_known: 1,
    cache_write: 10,
    cache_write_known: 1,
    output: 30,
    output_known: 1,
    total: 0,
    total_known: 0,
    final: 1,
  };
  const render = (totals: typeof t, historical = true) =>
    renderToStaticMarkup(<UsageSummary totals={totals} title="test" historical={historical} />);
  const missing = render(t);
  expect(missing).toContain("这些请求未提供总量");
  expect(missing).toContain("120");
  expect(missing).toContain("30");
  expect(missing).not.toContain("150");
  expect(missing).not.toContain("260");
  expect(render({ ...t, total_known: 1 })).toContain("<strong>0</strong>");
  expect(render({ ...t, requests: 0 })).toContain("当前筛选下暂无已采集请求");
  expect(render({ ...t, requests: 0 }, false)).toContain("此时段暂无可归属请求");
  const partial = render({ ...t, requests: 4, total: 3300, total_known: 3 });
  expect(partial).toContain("3,300");
  expect(partial).toContain("3/4");
  expect(partial).toContain("部分");
});
