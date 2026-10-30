import type { Locator, Page } from "playwright";

export async function openIssueAux(page: Page, label: string): Promise<Locator> {
  const name =
    ({ 跟进与交付: "跟进与外部交付", 历史: "变更历史" } as Record<string, string>)[label] ?? label;
  const existing = page.getByRole("dialog", { name, exact: true });
  if (await existing.isVisible()) return existing;
  const previous = page.getByRole("dialog");
  if (await previous.count()) {
    const last = previous.last();
    await last.getByRole("button", { name: "关闭详情", exact: true }).click();
    await last.waitFor({ state: "hidden" });
  }
  const region = page.getByRole("region", { name: "Issue详情", exact: true });
  if (["编辑定义", "交接负责人", "创建任务"].includes(name)) {
    const more = region.getByLabel("更多Issue操作");
    if ((await more.locator("..").getAttribute("open")) === null) await more.click();
    await region.getByRole("button", { name, exact: true }).click();
  } else {
    const side = await issueSidebar(page);
    await side
      .getByRole("button", {
        name: new RegExp(`^${name === "全部执行交付" ? "查看全部交付" : name}`),
      })
      .click();
  }
  return page.getByRole("dialog", {
    name: ({ 编辑定义: "编辑Issue定义" } as Record<string, string>)[name] ?? name,
    exact: true,
  });
}
export async function issueSidebar(page: Page): Promise<Locator> {
  await page
    .getByRole("region", { name: "Issue详情", exact: true })
    .getByRole("heading", { level: 1 })
    .waitFor();
  const side = page.getByRole("complementary", { name: "Issue辅助信息", exact: true });
  if (await side.isVisible()) return side;
  const drawer = page.getByRole("dialog", { name: "Issue辅助信息", exact: true });
  if (!(await drawer.isVisible()))
    await page
      .getByRole("region", { name: "Issue详情", exact: true })
      .getByRole("button", { name: "详情", exact: true })
      .click();
  await drawer.waitFor({ state: "visible" });
  return drawer;
}
export async function closeIssue(page: Page): Promise<void> {
  for (const dialog of await page.getByRole("dialog").all()) {
    const close = dialog.getByRole("button", { name: "关闭详情", exact: true });
    if (await close.isVisible()) await close.click();
  }
  await page
    .getByRole("region", { name: "Issue详情", exact: true })
    .getByRole("button", { name: "返回Issue列表或上一项", exact: true })
    .click();
}
export async function openIssueDefinition(page: Page): Promise<void> {
  const dialogs = page.getByRole("dialog");
  if (await dialogs.count())
    await dialogs.last().getByRole("button", { name: "关闭详情", exact: true }).click();
  const summary = page
    .getByRole("region", { name: "Issue详情", exact: true })
    .locator("summary")
    .filter({ hasText: /^目标与完成条件$/ });
  if ((await summary.locator("..").getAttribute("open")) === null) await summary.click();
}
