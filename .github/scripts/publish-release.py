#!/usr/bin/env python3
"""Publish one complete immutable commit release; interrupted drafts are resumable."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile


def gh(*args):
    return subprocess.run(["gh", *args], check=True, capture_output=True, text=True).stdout


def publish(root, commit, repo):
    tag = "main-" + commit
    if len(commit) != 40 or any(c not in "0123456789abcdef" for c in commit):
        raise ValueError("invalid source commit")
    expected = set()
    for arch in ("amd64", "arm64"):
        name = f"tend-{tag}-linux-{arch}"
        expected.update((name + ".tar.gz", name + ".json"))
        metadata = json.loads((root / (name + ".json")).read_text())
        if metadata["commit"] != commit or metadata["arch"] != arch:
            raise ValueError("artifact source/platform mismatch")
    if {p.name for p in root.iterdir()} != expected:
        raise ValueError("incomplete or unexpected release assets")
    checksums = "".join(
        f"{hashlib.sha256((root / name).read_bytes()).hexdigest()}  {name}\n"
        for name in sorted(expected)
    )
    (root / "SHA256SUMS").write_text(checksums)
    expected.add("SHA256SUMS")
    # Only a definite 404 permits creation; network/auth errors remain failures.
    response = subprocess.run(
        ["gh", "api", f"repos/{repo}/releases/tags/{tag}"], capture_output=True, text=True
    )
    if response.returncode:
        if "HTTP 404" not in response.stderr:
            raise RuntimeError(response.stderr)
        gh("release", "create", tag, "--repo", repo, "--target", commit,
           "--draft", "--prerelease", "--title", tag,
           "--notes", f"Complete Linux distributions built and checked from {commit}.")
        release = json.loads(gh("api", f"repos/{repo}/releases/tags/{tag}"))
    else:
        release = json.loads(response.stdout)
    reference = json.loads(gh("api", f"repos/{repo}/git/ref/tags/{tag}"))
    if reference["object"]["type"] != "commit" or reference["object"]["sha"] != commit:
        raise ValueError("release tag does not identify the checked source")
    assets = {a["name"] for a in release["assets"]}
    if assets - expected:
        raise ValueError("release contains unexpected assets")
    for name in sorted(expected):
        if name not in assets:
            if not release["draft"]:
                raise ValueError("published release is incomplete")
            gh("release", "upload", tag, str(root / name), "--repo", repo)
        with tempfile.TemporaryDirectory() as tmp:
            gh("release", "download", tag, "--repo", repo, "--pattern", name, "--dir", tmp)
            if (Path(tmp) / name).read_bytes() != (root / name).read_bytes():
                raise ValueError(f"published asset differs: {name}")
    if release["draft"]:
        gh("release", "edit", tag, "--repo", repo, "--draft=false", "--prerelease", "--latest=false")


if __name__ == "__main__":
    publish(Path(os.environ["RELEASE_ASSET_DIR"]), os.environ["GITHUB_SHA"], os.environ["GITHUB_REPOSITORY"])
