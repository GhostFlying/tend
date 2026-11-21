"""Reproduce the old restart decline and migrate the same disposable ledger."""
import contextlib
import hashlib
import json
import os
from pathlib import Path
import pwd
import secrets
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time

from controlled import prepare, write_private
from cross_version import eventually, stop


def seed_history(db, task):
    """Only setup is synthetic. The old and new services run their real upgrade paths."""
    run, lease = "run_" + task[5:], "lse_" + task[5:]
    now = int(time.time() * 1000)
    with sqlite3.connect(db) as ledger:
        ledger.execute("PRAGMA foreign_keys=ON")
        action, generation = ledger.execute(
            "SELECT action_id,generation FROM plan_actions WHERE task_id=? AND kind='implement'", (task,)).fetchone()
        ledger.execute("UPDATE plan_actions SET status='dispatched' WHERE action_id=?", (action,))
        ledger.execute("UPDATE tasks SET lifecycle='running' WHERE task_id=?", (task,))
        ledger.execute("""INSERT INTO runs(run_id,task_id,action_id,generation,role,host_id,desired_state,
                       observed_state,created_at,updated_at,protocol)
                       VALUES(?,?,?,?,'implement','compat','run','unknown',?,?,6)""",
                       (run, task, action, generation, now, now))
        ledger.execute("""INSERT INTO leases(lease_id,run_id,host_id,expires_at,status,created_at,updated_at)
                       VALUES(?,?,'compat',?,'lapsed',?,?)""", (lease, run, now - 1000, now, now))
        ledger.execute("INSERT INTO runner_journal_runs VALUES(?,'fixture-journal')", (run,))
    return run, lease


def exercise(root, old, first, second, seed):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        address = "127.0.0.1:" + str(sock.getsockname()[1])
    token = secrets.token_hex(32)
    env = dict(PATH=os.defpath, HOME=str(root), XDG_CONFIG_HOME=str(root / "config"),
               TEND_CONFIG_DIR=str(root / "config"), TEND_COMPAT_BARRIER=str(root),
               TEND_SERVER_SECRET=secrets.token_hex(32), TEND_HOST_TOKEN=token)
    for key in ("XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"):
        env[key] = os.environ[key]
    cfg = root / "server.json"
    db = root / "data/tend.db"
    write_private(cfg, json.dumps(dict(data_dir=str(db.parent), listen=address, local_owner="fixture",
                                      lease_ttl="24s", hosts=[dict(host_id="compat", os="linux", arch="amd64",
                                      slots=2, nodes=["tag:local"], token_hash=hashlib.sha256(token.encode()).hexdigest())])))
    runner = root / "runner.json"
    write_private(runner, json.dumps(dict(server_url="http://" + address, host_id="compat", home=str(root / "runner"),
                                         worker={"traex_bin": str(Path(__file__).with_name("model.py"))})))

    def rows(sql, args=()):
        if not db.exists():
            return []
        with contextlib.closing(sqlite3.connect(db.as_uri() + "?mode=ro", uri=True)) as conn:
            return conn.execute(sql, args).fetchall()

    def command(*args, **kwargs):
        return subprocess.check_output(args, env=env, timeout=180, **kwargs)

    def upgrade(*args):
        return json.loads(command(first / "tend", "upgrade", *args, "--socket", root / "server.sock", "--json"))

    def snapshot(task):
        return rows("""select run_id,lease_id,status,observed_state,expires_at,journal_id
                     from runs join leases using(run_id) join runner_journal_runs using(run_id) where task_id=?""", (task,))

    with contextlib.ExitStack() as stack:
        def launch(binary, path, logfile, extra=None):
            log = stack.enter_context((root / logfile).open("a"))
            proc = subprocess.Popen([binary, "-config", path], env=dict(env, **(extra or {})), stdout=log, stderr=log)
            stack.callback(stop, proc)
            return proc

        db_init = launch(old / "tend-server", cfg, "db-init.log")
        eventually("old ledger initialized", lambda: rows("select name from sqlite_master where name='hosts'")
                   and rows("select host_id from hosts where host_id='compat'"), [db_init])
        stop(db_init)
        original_task, live_task = command(seed, db).decode().splitlines()
        run, lease = seed_history(db, original_task)
        original = snapshot(original_task)
        subprocess.run(["systemctl", "--user", "import-environment", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"],
                       env=env, check=True, timeout=20)
        old_pid = prepare(root, old, old / "tend-server", cfg, env, stack, recovery_fixture=True)
        active = launch(old / "tend-runner", runner, "runner.log",
                        {"TEND_RUNNER_JOURNAL_ID": "fixture-journal", "TEND_UPDATER_PROCESS_INSTANCE_ID": "fixture-journal"})
        eventually("one live worker in the original journal", lambda: len(list(root.glob("*.gate"))) == 1, [active])
        live = snapshot(live_task)
        assert len(live) == 1 and live[0][2] == "active"

        refused = upgrade("start", "--release", first, "--server-only", "--upgrade-id", "old-guard")
        error = f"run {run} must reconcile before server restart"
        eventually("old guard rejected normal server replacement", lambda: error in (root / "initial.log").read_text(),
                   [active], timeout=100)
        state = json.loads((root / "updater/state.json").read_text())
        assert state["process"]["pid"] == old_pid
        declined = upgrade("status")
        assert declined["upgrade_id"] == refused["upgrade_id"] and declined["phase"] == "server_draining"
        assert snapshot(original_task) == original
        cancelled = upgrade("cancel", "--upgrade-id", "old-guard", "--revision", str(declined["revision"]))
        assert cancelled["status"] == "cancelled"
        print(f"38ea guard refused replacement for {run}/{lease}, old process retained", flush=True)

        recovery = (first / "tend", "upgrade", "recover", "--release", first, "--config", root / "updater.json",
                    "--unit", f"tend-{root.name}.service", "--request-id", "fixture-migration")
        preview = json.loads(command(*recovery, "--force", "--dry-run"))
        assert preview
        recovered = json.loads(command(*recovery, "--force"))
        readback = json.loads(command(*recovery, "--status"))
        assert recovered["phase"] == readback["phase"] == "complete"
        installed = json.loads((root / "updater/state.json").read_text())
        assert installed["process"]["pid"] != old_pid
        assert installed["committed_proof"]["external_recovery"] and not Path(f"/proc/{old_pid}").exists()
        assert snapshot(original_task) == original
        print("supported external recovery committed; historical Run and lease preserved", flush=True)

        batch = upgrade("start", "--release", second, "--server-only", "--upgrade-id", "corrected-guard")
        eventually("ordinary cutover with the same historical state", lambda: rows(
            "select upgrade_id from upgrades where upgrade_id=? and status='completed'", (batch["upgrade_id"],)),
            [active], timeout=185)
        state = json.loads((root / "updater/state.json").read_text())
        assert state["operation"]["id"] == batch["upgrade_id"] and state["operation"]["phase"] == "committed"
        assert state["process"]["pid"] != installed["process"]["pid"]
        assert snapshot(original_task) == original
        assert snapshot(live_task)[0][:3] == live[0][:3]
        assert len(list(root.glob("*.gate"))) == 1
        assert rows("select count(*) from runs") == [(2,)]
        assert rows("select count(*) from runner_window_released_leases") == [(0,)]
        gate, = root.glob("*.gate")
        with os.fdopen(os.open(gate, os.O_WRONLY | os.O_NONBLOCK), "w") as stream:
            print("complete", file=stream)
        eventually("original executor committed its result", lambda: rows(
            "select artifact_id from artifacts where run_id=?", (live[0][0],)), [active])
        assert snapshot(original_task) == original
        print("corrected guard committed another normal cutover with one original executor and no historical replay", flush=True)


if __name__ == "__main__":
    if pwd.getpwuid(os.getuid()).pw_name != "tend-wire-fixture":
        raise RuntimeError("the recovery fixture requires the disposable service account")
    with tempfile.TemporaryDirectory(prefix="tend-history-", dir="/tmp") as temp:
        root = Path(temp)
        try:
            exercise(root, *map(Path, sys.argv[1:]))
        except BaseException:
            for path in root.glob("*.log"):
                print(f"{path.name}\n" + path.read_text()[-8000:])
            raise
