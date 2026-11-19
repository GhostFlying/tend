"""Three real runner processes on one disposable runner host."""
import contextlib
import hashlib
import json
import os
from pathlib import Path
import pwd
import secrets
import shutil
import socket
import socketserver
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler
import select

from controlled import prepare, write_private
from cross_version import eventually, stop


class LocalDial(BaseHTTPRequestHandler):
    def do_POST(self):
        if (self.path != "/localapi/v0/dial" or self.headers.get("Authorization")
                or self.headers.get("Dial-Host") != "127.0.0.1"
                or self.headers.get("Dial-Port") != str(self.server.port)):
            self.send_error(403)
            return
        with socket.create_connection(("127.0.0.1", self.server.port)) as upstream:
            self.connection.sendall(b"HTTP/1.1 101 Switching Protocols\r\nUpgrade: ts-dial\r\nConnection: Upgrade\r\n\r\n")
            self.server.dials += 1
            while True:
                ready, _, _ = select.select([self.connection, upstream], [], [], 2)
                for source in ready:
                    data = source.recv(65536)
                    if not data:
                        return
                    (upstream if source is self.connection else self.connection).sendall(data)


def runner_unit(root, old, bundle, config, env, stack):
    state = root / "supervisor"
    runner_env = root / "service.env"
    write_private(runner_env, "".join(f"{k}={v}\n" for k, v in env.items()))
    cfg = root / "updater.json"
    bridge = root / "bridge.sock"
    write_private(cfg, json.dumps({
        "role": "runner", "instance_id": "compat", "initial_binary": str(old / "tend-runner"),
        "args": ["-config", str(config)], "env_files": [str(runner_env)], "state_dir": str(state),
        "log_path": str(root / "runner.log"), "authority_socket": str(bridge),
        "authorize_url": "http://localhost/updater/authorize", "health_url": "http://localhost/updater/health",
        "protocol": 5, "storage_version": 1,
    }))
    fixed = root / "tend-updater"
    shutil.copy2(old / "tend-updater", fixed)
    unit = root / ("tend-runner-" + root.parent.name + ".service")
    write_private(unit, f"""[Service]
Type=simple
KillMode=process
Restart=on-failure
Environment=XDG_RUNTIME_DIR=%t
Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=%t/bus
ExecStart={fixed} serve --config {cfg}
ExecStartPost=/usr/bin/python3 {Path(__file__).with_name('controlled.py').resolve()} {old}/tend-runner {config} {runner_env}
""")
    def systemctl(*args, check=True):
        subprocess.run(["systemctl", "--user", *args], timeout=40, check=check)
    systemctl("link", str(unit))
    stack.callback(lambda: systemctl("stop", unit.name, check=False))
    systemctl("start", unit.name)
    initial = int((root / "initial.pid").read_text())
    eventually("old supervisor listening", lambda: (state / "control.sock").is_socket(), [])
    subprocess.run([fixed, "bootstrap", "--config", cfg, "--pid", str(initial)], check=True,
                   timeout=30, stdout=subprocess.DEVNULL)
    return state, initial


def exercise(root, old, first, third, seed):
    assert pwd.getpwuid(os.getuid()).pw_name == "tend-wire-fixture"
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    address = f"127.0.0.1:{port}"
    token = secrets.token_hex(32)
    env = dict(PATH=os.defpath, HOME=str(root), XDG_CONFIG_HOME=str(root / "config"), TEND_CONFIG_DIR=str(root / "config"),
               TEND_COMPAT_BARRIER=str(root), TEND_SERVER_SECRET=secrets.token_hex(32), TEND_HOST_TOKEN=token)
    # The local bootstrap queries the disposable user's existing service manager.
    for key in ("XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"):
        env[key] = os.environ[key]
    rc = root / "host"
    rc.mkdir(mode=0o700)
    runner_config = rc / "runner.json"
    runner = dict(server_url="http://" + address, host_id="compat", home=str(rc / "home"),
                  worker={"traex_bin": str(Path(__file__).with_name("model.py").resolve())},
                  updater_socket=str(rc / "bridge.sock"), updater_control_socket=str(rc / "supervisor/control.sock"),
                  updater_bundle_dir=str(rc / "downloads"))
    write_private(runner_config, json.dumps(runner))
    server_config = root / "server.json"
    write_private(server_config, json.dumps(dict(data_dir=str(root / "data"), listen=address,
                  local_owner="fixture", lease_ttl="75s", hosts=[dict(host_id="compat", os="linux", arch="amd64",
                  slots=3, nodes=["tag:local"], token_hash=hashlib.sha256(token.encode()).hexdigest())])))
    db = root / "data/tend.db"

    def rows(sql, args=()):
        with contextlib.closing(sqlite3.connect(db.as_uri() + "?mode=ro", uri=True)) as c:
            return c.execute(sql, args).fetchall()

    def run(*args, timeout=120):
        return subprocess.check_output(args, env=env, timeout=timeout)

    def batch_done(batch):
        status = rows("select status,phase,state_json from upgrades where upgrade_id=?", (batch,))
        if status and status[0][0] == "attention":
            view = json.loads(status[0][2])["view"]
            raise RuntimeError(f"batch entered attention: {view.get('phase')} {view.get('reason')} {view.get('hosts')}")
        return status and status[0][0] == "completed"

    with contextlib.ExitStack() as stack:
        log = stack.enter_context((root / "old-server.log").open("w"))
        original = subprocess.Popen([old / "tend-server", "-config", server_config], env=env, stdout=log, stderr=log)
        stack.callback(stop, original)
        registration = subprocess.Popen([old / "tend-runner", "-config", runner_config], env=env, stdout=log, stderr=log)
        stack.callback(stop, registration)
        eventually("historical runner advertises task capabilities",
                   lambda: db.exists() and rows("select name from sqlite_master where name='host_runtime'")
                   and rows("select instance_id from host_runtime where json_extract(runtime_json,'$.protocol')=5"), [original, registration])
        stop(original)
        stop(registration)
        tasks = run(seed, db, "rolling").decode().splitlines()
        assert len(tasks) == 4
        prepare(root, first, first / "tend-server", server_config, env, stack)
        cli = first / "tend"
        run(cli, "login", "--server", "http://" + address)
        for task in tasks[1:]:
            run(cli, "pause", task)
        state, oldpid = runner_unit(rc, old, first, runner_config, env, stack)
        gates = lambda: set(root.glob("*.gate"))
        eventually("first long run", lambda: len(gates()) == 1, [])
        first_gate, = gates()
        lease = lambda task: rows("select r.run_id,l.lease_id,l.status from runs r join leases l using(run_id) where task_id=?", (task,))
        original_lease, = lease(tasks[0])
        daemon_sock = str(rc / "tailscaled.sock")
        class Daemon(socketserver.ThreadingUnixStreamServer):
            daemon_threads = True
        daemon = Daemon(daemon_sock, LocalDial)
        daemon.port, daemon.dials = port, 0
        threading.Thread(target=daemon.serve_forever, daemon=True).start()
        stack.callback(daemon.server_close)
        stack.callback(daemon.shutdown)
        runner["side_channel"] = {"kind": "tailscaled", "socket": daemon_sock}
        write_private(runner_config, json.dumps(runner))

        def start(bundle):
            view = json.loads(run(cli, "upgrade", "start", "--socket", root / "server.sock", "--release", bundle, "--json"))
            return view["upgrade_id"]

        batch = start(first)
        def ready():
            return rows("""select 1 from host_runtime h join host_controls c using(host_id) where
                json_extract(h.runtime_json,'$.upgrade_id')=? and json_extract(h.runtime_json,'$.mode')='maintenance'
                and json_extract(h.runtime_json,'$.applied_control_revision')=c.revision
                and exists(select 1 from upgrades where upgrade_id=? and phase='runners' and json_extract(state_json,'$.view.hosts[0].phase')='draining')""", (batch, batch))
        eventually("old runner ready for overlap", ready, [])
        args = [cli, "upgrade", "runner-handoff", "--config", runner_config, "--upgrade-id", batch, "--release", first]
        run(*args)
        eventually("first rolling batch committed", lambda: batch_done(batch), [])
        run(*args)
        assert lease(tasks[0]) == [original_lease] and Path(f"/proc/{oldpid}").exists()
        read_state = lambda: json.loads((state / "state.json").read_text())
        middle = read_state()["process"]
        run(cli, "resume", tasks[1])
        eventually("middle runner owns a long run", lambda: len(gates()) == 2, [])
        middle_gate, = gates() - {first_gate}
        middle_lease, = lease(tasks[1])
        batch = start(third)
        eventually("automatic third generation committed", lambda: batch_done(batch), [])
        current = read_state()
        assert len(current["draining_processes"]) == 2
        assert middle["pid"] != current["process"]["pid"] != oldpid and daemon.dials > 0
        assert lease(tasks[0]) == [original_lease] and lease(tasks[1]) == [middle_lease]
        run(cli, "resume", tasks[2])
        eventually("third runner owns its own work", lambda: len(gates()) == 3, [])
        assert rows("select journal_id from runner_journal_runs where run_id=?", (lease(tasks[2])[0][0],)) == [(current["current_journal"],)]
        run(cli, "resume", tasks[3])
        assert not lease(tasks[3])
        for task, gate, pid in [(tasks[0], first_gate, oldpid), (tasks[1], middle_gate, middle["pid"])]:
            with os.fdopen(os.open(gate, os.O_WRONLY | os.O_NONBLOCK), "w") as f:
                print("complete", file=f)
            eventually("original report committed", lambda: rows("select count(*) from artifacts where run_id=?", (lease(task)[0][0],)) == [(1,)], [])
            eventually("exact drained process acknowledged", lambda: rows(
                "select 1 from runner_retirements where confirmed_at is not null and json_extract(process_json,'$.pid')=?", (pid,)), [])
            assert not any(p["process"]["pid"] == pid for p in read_state().get("draining_processes", {}).values()) or eventually(
                "local stop acknowledgment", lambda: not any(p["process"]["pid"] == pid for p in read_state().get("draining_processes", {}).values()), [])
            assert read_state()["process"] == current["process"]
        print("three generations, original lease results, shared capacity and exact retirement verified", flush=True)


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="tend-gen-", dir="/tmp") as tmp:
        root = Path(tmp)
        try:
            exercise(root, *map(Path, sys.argv[1:]))
        except BaseException:
            for path in [*root.glob("*.log"), *root.glob("host/*.log"), *root.glob("host/supervisor/state.json")]:
                print(path.name, path.read_text()[-3500:], flush=True)
            raise
