"""Measure one or two hosted runners over the same four Go race shards."""
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import hashlib
import json
import os
import re
import subprocess
import time

output = Path(os.environ["RUNNER_TEMP"]) / "app-race"
output.mkdir()
arm = os.environ["BENCH_ARM"]
lane = int(os.environ["BENCH_LANE"])
lanes = int(os.environ["BENCH_LANES"])
cpus = len(os.sched_getaffinity(0))
cpu = next(line.split(":", 1)[1].strip()
           for line in Path("/proc/cpuinfo").read_text().splitlines()
           if line.startswith("model name"))
env = dict(os.environ, GOFLAGS="-p=1", GOMAXPROCS=str(cpus),
           BENCH_ROOTS=str(output / "roots.txt"), BENCH_PATTERNS=str(output / "patterns.json"))
summary = {"arm": arm, "cpus": cpus, "cpu_model": cpu, "lane": lane, "lanes": lanes,
           "go": subprocess.check_output(["go", "version"], text=True).strip(),
           "source": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()}
started = time.monotonic()

def measure(index, pattern, procs, roots):
    target = output / f"app-{index + 1}.jsonl"
    env_test = dict(env, GOMAXPROCS=str(procs))
    start = time.monotonic()
    with target.open("w") as log:
        child = subprocess.run(["go", "test", "-race", "-timeout", "60m", "-count=1",
                                "-json", "-run", pattern, "./internal/app"],
                               env=env_test, check=False, stdout=log, stderr=subprocess.STDOUT)
    selected = Counter(name for name in roots if re.fullmatch(pattern, name))
    ran, ended = Counter(), Counter()
    for line in target.read_text().splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        name = event.get("Test", "")
        if not name or "/" in name:
            continue
        if event["Action"] == "run":
            ran[name] += 1
        if event["Action"] in ("pass", "fail", "skip"):
            ended[name] += 1
    result = {"index": index, "seconds": round(time.monotonic() - start, 2),
              "root_count": len(ran), "exit": child.returncode,
              "coverage": bool(ran and ran == ended == selected)}
    print(json.dumps(result), flush=True)
    return result

try:
    with (output / "roots.txt").open("w") as roots_file:
        subprocess.run(["go", "test", "-race", "-list", ".", "./internal/app"],
                       env=env, check=True, stdout=roots_file)
    subprocess.run(["go", "test", "-count=1", "-run", "^TestExperimentAppRunnerShards$",
                    "./internal/testkit/racecheck"], env=env, check=True)
    patterns = json.loads((output / "patterns.json").read_text())
    roots = [n.strip() for n in (output / "roots.txt").read_text().splitlines()
             if n.strip().startswith(("Test", "Example", "Fuzz"))]
    if len(patterns) != 4 or any(sum(bool(re.fullmatch(p, n)) for p in patterns) != 1 for n in roots):
        raise RuntimeError("the four patterns must partition discovery")
    assigned = list(enumerate(patterns))[lane::lanes]
    procs = max(1, cpus // len(assigned))
    summary.update(root_count=len(roots), shards=[index for index, _ in assigned],
                   test_gomaxprocs=procs, preparation_seconds=round(time.monotonic()-started, 2),
                   pattern_sha256=hashlib.sha256((output / "patterns.json").read_bytes()).hexdigest())
    with ThreadPoolExecutor(max_workers=len(assigned)) as workers:
        futures = [workers.submit(measure, index, pattern, procs, roots) for index, pattern in assigned]
        results = [future.result() for future in futures]
    summary["results"] = results
    if not all(r["exit"] == 0 and r["coverage"] for r in results):
        raise RuntimeError("one or more assigned shards failed or were incomplete")
finally:
    summary["seconds"] = round(time.monotonic()-started, 2)
    (output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
