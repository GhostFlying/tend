"""Temporary same-runner App comparison; never part of the regular gate."""
import collections
import datetime
import hashlib
import json
import os
from pathlib import Path
import resource
import shutil
import subprocess
import time

output = Path(os.environ['RUNNER_TEMP']) / 'app-cost-pair'
output.mkdir()
shutil.copyfile(__file__, output / 'driver.py')
probe = output / 'runtime.go'
probe.write_text('package main\nimport("encoding/json";"os";"runtime")\nfunc main(){json.NewEncoder(os.Stdout).Encode(map[string]any{"Go":runtime.Version(),"CPUs":runtime.NumCPU(),"GOMAXPROCS":runtime.GOMAXPROCS(0)})}\n')
runtime = json.loads(subprocess.check_output(['go', 'run', str(probe)], text=True))
models = [line.split(':', 1)[1].strip() for line in Path('/proc/cpuinfo').read_text().splitlines() if line.startswith('model name')]
quota = Path('/sys/fs/cgroup/cpu.max')
record = {'StartedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'Runtime': runtime, 'CPUModel': models[0] if models else None, 'CPUQuota': quota.read_text().strip() if quota.exists() else None, 'Arms': []}


def persist():
    (output / 'summary.json').write_text(json.dumps(record, indent=2) + '\n')


def measure(label, source, procs=None):
    subprocess.run(['git', 'checkout', '--detach', source], check=True)
    assert subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip() == source
    assert not subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=no'], text=True)
    env = os.environ.copy()
    if procs is not None:
        env['GOMAXPROCS'] = str(procs)
    arm = {'Label': label, 'Source': source, 'ProcessGOMAXPROCS': procs or runtime['GOMAXPROCS'], 'AppShards': 4, 'Count': 1, 'LoadBefore': os.getloadavg()}
    record['Arms'].append(arm)
    started = time.monotonic()
    with (output / (label + '-prepare.log')).open('w') as log:
        prepared = subprocess.run(['go', 'test', '-race', '-run=^$', '-count=1', '-p', '2', './internal/app'], env=env, stdout=log, stderr=subprocess.STDOUT)
    arm['PreparationSeconds'] = time.monotonic() - started
    arm['PreparationExitCode'] = prepared.returncode
    persist()
    if prepared.returncode:
        raise RuntimeError(label + ' preparation failed')
    reports = output / (label + '-events')
    before = resource.getrusage(resource.RUSAGE_CHILDREN)
    started = time.monotonic()
    with (output / (label + '.log')).open('w') as log:
        tested = subprocess.run(['go', 'run', './internal/testkit/racecheck', '-shards', '4', '-suite-shards', '1', '-count', '1', '-group', 'app', '-report-dir', str(reports), './...'], env=env, stdout=log, stderr=subprocess.STDOUT)
    after = resource.getrusage(resource.RUSAGE_CHILDREN)
    arm.update(Seconds=time.monotonic()-started, ExitCode=tested.returncode, UserSeconds=after.ru_utime-before.ru_utime, SystemSeconds=after.ru_stime-before.ru_stime, LoadAfter=os.getloadavg())
    runs = collections.Counter()
    ends = collections.Counter()
    failures = []
    for file in reports.rglob('*.jsonl'):
        for line in file.open():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            name = event.get('Test', '')
            if event.get('Action') == 'fail':
                failures.append(name or event.get('Package'))
            if not name or '/' in name:
                continue
            if event['Action'] == 'run':
                runs[name] += 1
            if event['Action'] in ('pass', 'fail', 'skip'):
                ends[name] += 1
    arm.update(RootCount=len(runs), RootSHA256=hashlib.sha256('\n'.join(sorted(runs)).encode()).hexdigest(), Failures=failures, SourceUnchanged=not subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=no'], text=True))
    arm['Complete'] = tested.returncode == 0 and not failures and len(runs) == 1032 and runs == ends and all(n == 1 for n in runs.values()) and arm['SourceUnchanged']
    persist()
    print(json.dumps(arm), flush=True)
    if not arm['Complete']:
        raise RuntimeError(label + ' incomplete or failed')
    return arm


try:
    before = measure('before', '7970ab961a1f1f636c8bc067972ef6a64fc941d5')
    after = measure('after', '1c2da88a2734cb27f42d334c7463108f22cd1cf3')
    assert before['RootSHA256'] == after['RootSHA256']
    if after['Seconds'] > before['Seconds'] * 1.05:
        bounded = measure('after-bounded', after['Source'], procs=1)
        assert bounded['RootSHA256'] == before['RootSHA256']
    else:
        record['BoundedArm'] = 'Not needed: no same-runner regression above five percent'
finally:
    record['FinishedAt'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    persist()
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as summary:
        summary.write('```json\n' + json.dumps(record, indent=2) + '\n```\n')
