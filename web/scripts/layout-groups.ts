import { spawnSync } from "node:child_process";
import { resolve, join } from "node:path";
import { pathToFileURL } from "node:url";

// Balance the mandatory regressions by observed runtime. The copy-only helper
// is a local diagnostic subset of issue-surfaces, not another CI test root.
export const layoutGroups: Record<string, string[]> = {
  "1": [
    "test:ui-tour",
    "test:issue-surfaces-layout",
    "test:decision-layout",
    "test:work-items-layout",
    "test:agent-directory",
    "test:delivery-refresh",
    "test:navigation-layout",
  ],
  "2": [
    "test:issue-detail-layout",
    "test:usage",
    "test:layout",
    "test:repos-layout",
    "test:agents",
    "test:followups",
    "test:inbox-badge",
  ],
};

function run(group: string): number {
  const commands = layoutGroups[group];
  if (!commands) throw new Error(`Unknown layout group: ${group}`);
  for (const command of commands) {
    const result = spawnSync("pnpm", [command], {
      stdio: "inherit",
      env: {
        ...process.env,
        TEND_LAYOUT_OUTPUT: join(
          process.env.TEND_LAYOUT_OUTPUT ?? process.env.RUNNER_TEMP ?? ".local",
          command.replace("test:", "tend-"),
        ),
      },
    });
    if (result.error) throw result.error;
    if (result.status !== 0) return result.status ?? 1;
  }
  return 0;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  process.exitCode = run(process.argv[2] ?? "");
}
