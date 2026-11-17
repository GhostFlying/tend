import { expect, test } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { conversationStatus } from "./activityStatus.ts";
import { LiveStatus } from "./LiveStatus.tsx";

test("terminal disclosure shows unknown duration without a durable end", () => {
  const html = renderToStaticMarkup(
    <LiveStatus status={conversationStatus([], "failed", 1000, null)}>
      <span>查看过程</span>
    </LiveStatus>,
  );
  expect(html).toContain("用时未知");
});

test("terminal disclosure uses the durable whole-turn end", () => {
  const html = renderToStaticMarkup(
    <LiveStatus status={conversationStatus([], "replied", 1000, 84000)}>
      <span>查看过程</span>
    </LiveStatus>,
  );
  expect(html).toContain("用时 1:23");
});
