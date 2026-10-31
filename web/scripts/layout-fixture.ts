import { spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { build } from "vite";

export function fixtureEnvironment(
  source: NodeJS.ProcessEnv,
  directory: string,
): NodeJS.ProcessEnv {
  return {
    ...Object.fromEntries(
      Object.entries(source).filter(
        ([key]) => !key.startsWith("ORCA_") && !/^(https?|all)_proxy$/i.test(key),
      ),
    ),
    TEND_LAYOUT_APP_ROOT: join(directory, "app"),
    TEND_LAYOUT_CACHE_DIR: join(directory, "cache"),
  };
}

async function run(script: string, args: string[]): Promise<number> {
  const directory = await mkdtemp(join(tmpdir(), "tend-layout-"));
  const root = fileURLToPath(new URL("../", import.meta.url));
  // Capture before Vite sets NODE_ENV so dev fixtures retain their original mode.
  const environment = fixtureEnvironment(process.env, directory);
  for (const key of Object.keys(process.env)) {
    if (!(key in environment)) delete process.env[key];
  }
  let child: ChildProcess | undefined;
  let stopped: number | undefined;
  let grace: ReturnType<typeof setTimeout> | undefined;
  const kill = (signal: NodeJS.Signals) => {
    if (!child?.pid) return;
    try {
      if (process.platform === "win32") child.kill(signal);
      else process.kill(-child.pid, signal);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error;
    }
  };
  const stop = (code: number) => {
    stopped = code;
    kill("SIGTERM");
    grace ??= setTimeout(() => kill("SIGKILL"), 5_000).unref();
  };
  const interrupt = () => stop(130);
  const terminate = () => stop(143);
  process.on("SIGINT", interrupt);
  process.on("SIGTERM", terminate);
  try {
    await build({
      root,
      configFile: fileURLToPath(new URL("../vite.config.ts", import.meta.url)),
      build: { outDir: environment.TEND_LAYOUT_APP_ROOT! },
    });
    if (stopped !== undefined) return stopped;
    return await new Promise<number>((done, reject) => {
      child = spawn(process.execPath, [resolve(root, script), ...args], {
        cwd: root,
        env: environment,
        stdio: "inherit",
        detached: process.platform !== "win32",
      });
      child.once("error", reject);
      child.once("exit", (code) => done(stopped ?? code ?? 1));
    });
  } finally {
    clearTimeout(grace);
    process.off("SIGINT", interrupt);
    process.off("SIGTERM", terminate);
    await rm(directory, { recursive: true, force: true });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const script = process.argv[2];
  if (!script) throw new Error("Pass a layout fixture script to the runner");
  process.exitCode = await run(script, process.argv.slice(3));
}
