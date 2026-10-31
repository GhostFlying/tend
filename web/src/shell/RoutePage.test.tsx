import { renderToStaticMarkup } from "react-dom/server";
import { expect, test } from "vitest";
import { RouteErrorBoundary } from "./RoutePage.tsx";

test("the route boundary preserves normal route content", () => {
  const boundary = new RouteErrorBoundary({ identity: "/issues", children: <p>正常页面</p> });
  expect(renderToStaticMarkup(boundary.render())).toBe("<p>正常页面</p>");
});

test("a rejected route displays a compact reload fallback without exposing the error", () => {
  const boundary = new RouteErrorBoundary({ identity: "/issues", children: <p>正常页面</p> });
  boundary.state = { ...boundary.state, ...RouteErrorBoundary.getDerivedStateFromError() };
  const html = renderToStaticMarkup(boundary.render());
  expect(html).toContain('role="alert"');
  expect(html).toContain("页面已更新，需要重新加载。");
  expect(html).toContain("重新加载</button>");
  expect(html).not.toContain("正常页面");
  const next = RouteErrorBoundary.getDerivedStateFromProps(
    { identity: "/repos", children: <p>另一个页面</p> },
    boundary.state,
  );
  expect(next).toEqual({ identity: "/repos", failed: false });
  expect(RouteErrorBoundary.getDerivedStateFromProps(boundary.props, boundary.state)).toBeNull();
});

test("healthy route changes keep the content subtree rather than remounting it", () => {
  const child = <input aria-label="筛选" defaultValue="连续输入" />;
  const boundary = new RouteErrorBoundary({ identity: "/issues", children: child });
  const next = RouteErrorBoundary.getDerivedStateFromProps(
    { identity: "/issues?project=sample", children: child },
    boundary.state,
  );
  boundary.state = next!;
  expect(boundary.render()).toBe(child);
  expect(boundary.state.failed).toBe(false);
});
