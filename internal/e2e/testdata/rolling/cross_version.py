#!/usr/bin/env python3
"""Run the published runner, old server and candidate against an isolated ledger."""
import argparse
import contextlib
import controlled
import hashlib
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import sqlite3
import subprocess
import tempfile
import time


def eventually(label, read, processes):
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        for process in processes:
            if process.poll() is not None:
                raise RuntimeError(f"{label}: {process.args[0]} exited {process.returncode}")
        value = read()
        if value:
            return value
        time.sleep(0.1)
    raise TimeoutError(label)


def stop(process):
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
        try:
            process.wait(timeout=20)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
            raise


def exercise(root, old, candidate, seed, source, protocol, bundle=None):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        address = "127.0.0.1:" + str(sock.getsockname()[1])
    data = root / "data"
    db = data / "tend.db"
    token = secrets.token_hex(32)
    server_config = root / "server.json"
    server_config.write_text(json.dumps({
        "data_dir": str(data), "listen": address, "control_socket": str(root / "server.sock"),
        "local_owner": "fixture", "lease_ttl": "24s",
        "hosts": [{"host_id": "compat", "os": "linux", "arch": "amd64", "slots": 2,
                   "nodes": ["tag:local"], "token_hash": hashlib.sha256(token.encode()).hexdigest()}],
    }))
    runner_config = root / "runner.json"
    runner_config.write_text(json.dumps({
        "server_url": "http://" + address, "host_id": "compat", "home": str(root / "runner-home"),
        "worker": {"traex_bin": str(Path(__file__).with_name("model.py").resolve())},
    }))
    env = dict(PATH=os.defpath, HOME=str(root), TEND_HOST_TOKEN=token, TEND_SERVER_SECRET=secrets.token_hex(32),
               TEND_COMPAT_BARRIER=str(root), XDG_CONFIG_HOME=str(root / "config"), TEND_CONFIG_DIR=str(root / "config"))
    old_server = old / "tend-server"
    old_runner = old / "tend-runner"

    def rows(sql, args=()):
        if not db.exists():
            return []
        with contextlib.closing(sqlite3.connect(db.as_uri() + "?mode=ro", uri=True)) as c:
            if not c.execute("select 1 from sqlite_master where name='host_runtime'").fetchone():
                return []
            return c.execute(sql, args).fetchall()

    with contextlib.ExitStack() as stack:
        def launch(path, config):
            log = stack.enter_context((root / (path.name + ".log")).open("a"))
            proc = subprocess.Popen([path, "-config", config], env=env, stdout=log, stderr=log)
            stack.callback(stop, proc)
            return proc

        server = launch(old_server, server_config)
        runner = launch(old_runner, runner_config)
        identity = eventually("old runner registered", lambda: rows(
            "select instance_id from host_runtime where json_extract(runtime_json,'$.protocol')=?", (protocol,)), [server, runner])
        # Seed only business inputs using the matching historical app/store.
        stop(server)
        subprocess.run([seed, db], check=True, env=env)
        if bundle:
            stop(runner)
            initial_pid = controlled.prepare(root, bundle, candidate, server_config, env, stack)
            runner = launch(old_runner, runner_config)
            live = [runner]
        else:
            server = launch(old_server, server_config)
            live = [server, runner]
        eventually("both historical turns active", lambda: len(list(root.glob("*.gate"))) == 2, live)
        runs = rows("select run_id,lease_id from leases where status='active' order by run_id")
        assert len(runs) == 2
        if bundle:
            identity = rows("select instance_id from host_runtime")
            controlled.exercise(root, bundle, initial_pid, rows, runner, runs)
        else:
            stop(server)
        # Fixed restart coverage may already exceed the normal renewal TTL.
        # After controlled commitment, prove the new server actually processed
        # another renewal; the direct transport test still requires TTL growth.
        watermark = "updated_at" if bundle else "expires_at"
        before = rows(f"select run_id,{watermark} from leases order by run_id")
        if not bundle:
            server = launch(candidate, server_config)
            live = [server, runner]
        assert len(before) == len(runs)

        def renewed():
            current = dict(rows(f"select run_id,{watermark} from leases where status='active'"))
            return all(current.get(run, 0) > previous for run, previous in before)
        eventually("same leases renewed by candidate", renewed, live)
        assert rows("select instance_id from host_runtime") == identity
        assert rows("select run_id,lease_id from leases order by run_id") == runs
        gates = sorted(root.glob("*.gate"))
        assert len(gates) == 2
        fd = os.open(gates[0], os.O_WRONLY | os.O_NONBLOCK)
        with os.fdopen(fd, "w") as stream:
            print("complete", file=stream)
        completed = eventually("original result committed", lambda: rows(
            "select run_id from artifacts where run_id in (?,?)", [run for run, _ in runs]), live)
        assert len(completed) == 1
        cancel_run = next(run for run, _ in runs if run != completed[0][0])
        task, rev = rows("select t.task_id,t.control_revision from tasks t join runs r using(task_id) where r.run_id=?",
                         (cancel_run,))[0]
        subprocess.run([old / "tend", "login", "--server", "http://" + address], env=env, check=True)
        subprocess.run([old / "tend", "cancel", task, "--revision", str(rev), "--data", "{}"], env=env, check=True)
        eventually("historical runner stopped the cancelled run", lambda: rows(
            "select 1 from leases where run_id=? and status='released'", (cancel_run,)), live)
        assert (root / (gates[1].stem + ".interrupted")).exists(), "old worker did not receive the stop command"
        assert rows("select run_id,count(*) from artifacts where run_id in (?,?) group by run_id",
                    [run for run, _ in runs]) == [(completed[0][0], 1)]
        print(f"protocol {protocol} {source}: original process, renewed leases, delivered result and cancellation verified")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--historical-dir", type=Path, required=True)
    parser.add_argument("--candidate-server", type=Path, required=True)
    parser.add_argument("--seed", type=Path, required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--protocol", required=True, type=int)
    parser.add_argument("--controlled-bundle", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="tend-wire-", dir="/tmp") as temp:
        root = Path(temp)
        try:
            exercise(root, args.historical_dir.resolve(), args.candidate_server.resolve(), args.seed.resolve(), args.source, args.protocol, args.controlled_bundle)
        except BaseException:
            for path in root.glob("*.log"):
                print(f"{path.name}\n" + path.read_text()[-5000:])
            raise


if __name__ == "__main__":
    main()
