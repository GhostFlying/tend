"""Build an isolated server with the next protocol range for maintenance CI."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


def build(bundle, output):
    output.mkdir()
    for src in bundle.iterdir():
        if src.is_file():
            os.link(src, output / src.name)
    source = Path("internal/protocol/runner_runtime.go").resolve()
    overrides = output.parent / "wire-overlay"
    overrides.mkdir()
    replacement = overrides / "runner_runtime.go"
    manifest = json.loads((bundle / "manifest.json").read_text())
    next_protocol = {key: value + 1 for key, value in manifest["protocol"].items()}
    text = source.read_text()
    for key, name in [("min", "MinimumRunnerProtocol"), ("max", "CurrentRunnerProtocol")]:
        declaration = f"const {name} int64 = {manifest['protocol'][key]}"
        assert text.count(declaration) == 1
        text = text.replace(declaration, f"const {name} int64 = {next_protocol[key]}")
    replacement.write_text(text)
    overlay = overrides / "overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(source): str(replacement)}}))
    role = "server"
    artifact, = (a for a in manifest["artifacts"] if a["role"] == role)
    binary = output / artifact["path"]
    binary.unlink()
    subprocess.run(["go", "build", "-trimpath", "-buildvcs=true", "-overlay", overlay, "-o", binary,
                    "./cmd/tend-server"], check=True, env={**os.environ, "CGO_ENABLED": "0"})
    with binary.open("rb") as f:
        sha = hashlib.file_digest(f, "sha256").hexdigest()
    for name in ("manifest.json", "release-set.json"):
        path = output / name
        doc = json.loads(path.read_bytes())
        doc["release_id"] += "-next-window-fixture"
        target, = (a for a in doc["artifacts"] if a["role"] == role)
        target.update(sha256=sha, size=binary.stat().st_size)
        if name == "manifest.json":
            doc["protocol"] = next_protocol
        else:
            doc["manifest_sha256"] = hashlib.sha256((output / "manifest.json").read_bytes()).hexdigest()
        path.unlink()
        path.write_text(json.dumps(doc))


if __name__ == "__main__":
    build(*map(Path, sys.argv[1:]))
