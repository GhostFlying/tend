"""Real service supervisor on a disposable CI account, with all Tend state in root."""
import json
import os
from pathlib import Path
import pwd
import subprocess
import sys
import time


def write_private(path, content):
    path.write_text(content)
    path.chmod(0o600)


def prepare(root, bundle, initial, server_config, env, stack):
    # The hosted job creates this account on a fresh VM. Refuse a developer's
    # service manager even if this module is invoked directly.
    if pwd.getpwuid(os.getuid()).pw_name != "tend-wire-fixture":
        raise RuntimeError("controlled fixture requires the disposable service account")
    control = root / "server.sock"
    state = root / "updater"
    config = root / "updater.json"
    manifest = json.loads((bundle / "manifest.json").read_text())
    server = json.loads(server_config.read_text())
    server["upgrades"] = {"instance": "fixture", "updater_socket": str(state / "control.sock"),
                          "control_socket": str(control), "release_dir": str(root / "releases")}
    write_private(server_config, json.dumps(server))
    server_env = root / "server.env"
    write_private(server_env, "".join(f"{key}={env[key]}\n" for key in
                                      ("HOME", "XDG_CONFIG_HOME", "TEND_SERVER_SECRET")))
    write_private(config, json.dumps({
        "instance_id": "fixture", "role": "server", "state_dir": str(state), "initial_binary": str(initial),
        "args": ["-config", str(server_config)], "env_files": [str(server_env)], "log_path": str(root / "service.log"),
        "authority_socket": str(control), "authorize_url": "http://localhost/updater/authorize",
        "health_url": "http://localhost/updater/health", "protocol": manifest["protocol"]["max"],
        "storage_version": manifest["storage"]["server_schema"]["max"],
    }))
    unit = root / ("tend-" + root.name + ".service")
    write_private(unit, f"""[Service]
Type=simple
KillMode=process
Restart=on-failure
Environment=XDG_RUNTIME_DIR=%t
Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=%t/bus
MemoryAccounting=yes
TasksAccounting=yes
ExecStart={bundle}/tend-updater serve --config {config}
ExecStartPost=/usr/bin/python3 {Path(__file__).resolve()} {initial} {server_config} {server_env}
""")
    def systemctl(*args, check=True):
        subprocess.run(["systemctl", "--user", *args], check=check, timeout=30)
    systemctl("link", str(unit))
    def cleanup():
        subprocess.run(["journalctl", "--user", "-u", unit.name, "-n", "20", "--no-pager"],
                       check=False, timeout=10)
        systemctl("stop", unit.name, check=False)
    stack.callback(cleanup)
    systemctl("start", unit.name)
    deadline = time.monotonic() + 30
    while not (state / "control.sock").is_socket():
        if time.monotonic() >= deadline:
            raise TimeoutError("private updater socket")
        time.sleep(0.1)
    # Inspect exactly the two processes owned by this private unit. The
    # supervisor validates both memberships during its first handoff.
    pid = int((root / "initial.pid").read_text())
    manager_pid = int(subprocess.check_output(["systemctl", "--user", "show", unit.name,
                                               "--property=MainPID", "--value"]))
    print("test unit", unit.name, "updater", Path(f"/proc/{manager_pid}/cgroup").read_text().strip(),
          "child", Path(f"/proc/{pid}/cgroup").read_text().strip(), flush=True)
    subprocess.run([bundle / "tend-updater", "bootstrap", "--config", config, "--pid", str(pid)],
                   stdout=subprocess.DEVNULL, check=True, timeout=30)
    deadline = time.monotonic() + 30
    while not control.is_socket():
        if time.monotonic() >= deadline:
            raise TimeoutError("initial server control listener")
        time.sleep(0.1)
    subprocess.run([bundle / "tend", "upgrade", "status", "--socket", control, "--json"],
                   stdout=subprocess.DEVNULL, check=True, timeout=15)
    return pid


def exercise(root, bundle, initial_pid, rows, runner, runs):
    cli = [bundle / "tend", "upgrade"]
    socket_args = ["--socket", str(root / "server.sock"), "--json"]
    admitted = json.loads(subprocess.check_output(
        [*cli, "start", *socket_args, "--server-only", "--release", bundle], timeout=120))
    batch = admitted["upgrade_id"]
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        result = rows("select status,phase from upgrades where upgrade_id=?", (batch,))
        if runner.poll() is not None or result and result[0][0] in ("attention", "cancelled"):
            raise RuntimeError(f"controlled upgrade stopped: {result}")
        if result and result[0][0] == "completed":
            break
        time.sleep(0.2)
    else:
        raise TimeoutError(f"controlled upgrade {batch} last observed {result}")
    view = json.loads(subprocess.check_output([*cli, "status", *socket_args], timeout=20))
    assert view["upgrade_id"] == batch and view["server_only"] and view["status"] == "completed"
    assert [(h["host_id"], h["phase"]) for h in view["hosts"]] == [("compat", "retained")]
    state = json.loads((root / "updater/state.json").read_text())
    operation = state["operation"]
    assert operation["id"] == batch and operation["phase"] == "committed" and operation["grant"]["allowed"]
    assert operation["previous_process"]["pid"] == initial_pid != state["process"]["pid"]
    assert operation["candidate_process"] == state["process"]
    assert operation["previous_process"]["sha256"] != state["process"]["sha256"]
    assert rows("select run_id,lease_id from leases where status='active' order by run_id") == runs
    print(f"controlled batch {batch} committed with both original runs active", flush=True)


if __name__ == "__main__":
    initial, config, env_file = map(Path, sys.argv[1:])
    root = config.parent
    # Start the initial server in the same private unit as the supervisor.
    launch = "fixture-initial"
    service_env = dict(line.split("=", 1) for line in env_file.read_text().splitlines())
    with (root / "initial.log").open("a") as log:
        process = subprocess.Popen([initial, "-config", config], stdout=log, stderr=log,
                                   env=dict(os.environ | service_env, TEND_UPDATER_LAUNCH_ID=launch,
                                            TEND_UPDATER_PROCESS_INSTANCE_ID=launch))
    (root / "initial.pid").write_text(str(process.pid))
