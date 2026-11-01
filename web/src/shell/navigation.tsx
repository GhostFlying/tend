import type { ComponentType, SVGProps } from "react";
import type { Route } from "../app/router.ts";
import { Chat, Pending, Tasks, Run, Gear, Repository, Usage, Host, More } from "./icons.tsx";

export type NavItem = { path: string; label: string; Icon: ComponentType<SVGProps<SVGSVGElement>> };
export const groups: { label: string; items: NavItem[] }[] = [
  {
    label: "对话",
    items: [
      { path: "/", label: "对话", Icon: Chat },
      { path: "/inbox", label: "收件箱", Icon: Pending },
    ],
  },
  {
    label: "工作",
    items: [
      { path: "/issues", label: "Issues", Icon: Tasks },
      { path: "/tasks", label: "任务执行", Icon: Run },
    ],
  },
  {
    label: "资源",
    items: [
      { path: "/repos", label: "仓库", Icon: Repository },
      { path: "/usage", label: "Token 用量", Icon: Usage },
      { path: "/hosts", label: "主机", Icon: Host },
    ],
  },
];
export const diagnostics: NavItem = {
  path: "/diagnostics/main",
  label: "主 Agent 诊断",
  Icon: Run,
};
export const settings: NavItem = { path: "/settings", label: "设置", Icon: Gear };
export const phoneTabs: NavItem[] = [
  { path: "/", label: "对话", Icon: Chat },
  groups[1]!.items[0]!,
  groups[0]!.items[1]!,
  { path: "/more", label: "更多", Icon: More },
];
export const moreItems = [groups[1]!.items[1]!, ...groups[2]!.items, diagnostics, settings];

export function navActive(route: Route, path: string): boolean {
  const pages: Record<string, string[]> = {
    "/": ["im"],
    "/diagnostics/main": ["chat", "conversation"],
    "/inbox": ["inbox", "attention"],
    "/issues": ["issues", "issue"],
    "/tasks": ["tasks", "task"],
    "/hosts": ["hosts", "upgrade"],
    "/settings": ["settings", "agents", "archive"],
    "/more": ["more", "tasks", "task", "repos", "usage", "hosts", "settings", "agents", "archive"],
  };
  return (pages[path] ?? [path.slice(1)]).includes(route.page);
}
