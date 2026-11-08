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


def lookup_release(repo, tag):
    owner, name = repo.split("/", 1)
    query = """query($owner:String!,$name:String!,$tag:String!) {
      repository(owner:$owner,name:$name) {
        release(tagName:$tag) {
          isDraft tagCommit { oid } releaseAssets(first:100) { nodes { name } }
        }
      }
    }"""
    result = json.loads(gh("api", "graphql", "-f", "query=" + query,
                           "-f", "owner=" + owner, "-f", "name=" + name, "-f", "tag=" + tag))
    repository = result["data"]["repository"]
    if repository is None:
        raise ValueError("release repository is unavailable")
    return repository["release"]


def ensure_tag(repo, tag, commit):
    endpoint = f"repos/{repo}/git/ref/tags/{tag}"
    response = subprocess.run(["gh", "api", endpoint], capture_output=True, text=True)
    if response.returncode:
        if "HTTP 404" not in response.stderr:
            raise RuntimeError(response.stderr)
        # Draft creation does not create a Git reference. Create and verify it
        # explicitly; an uncertain POST is reconciled on the next invocation.
        gh("api", f"repos/{repo}/git/refs", "--method", "POST",
           "-f", "ref=refs/tags/" + tag, "-f", "sha=" + commit)
        reference = json.loads(gh("api", endpoint))
    else:
        reference = json.loads(response.stdout)
    if reference["object"]["type"] != "commit" or reference["object"]["sha"] != commit:
        raise ValueError("release tag does not identify the checked source")


def publish(root, commit, repo):
    tag = "main-" + commit
    if len(commit) != 40 or any(c not in "0123456789abcdef" for c in commit):
        raise ValueError("invalid source commit")
    expected = set()
    for arch in ("amd64", "arm64", "fleet"):
        name = f"tend-{tag}-linux-{arch}"
        expected.update((name + ".tar.gz", name + ".json"))
        metadata = json.loads((root / (name + ".json")).read_text())
        target_arch = "multi" if arch == "fleet" else arch
        if metadata["commit"] != commit or metadata["arch"] != target_arch:
            raise ValueError("artifact source/platform mismatch")
    for arch in ("amd64", "arm64"):
        name = f"tend-maintainer-{tag}-linux-{arch}"
        expected.update((name + ".tar.gz", name + ".json"))
        metadata = json.loads((root / (name + ".json")).read_text())
        artifact = metadata["artifact"]
        if metadata["commit"] != commit or artifact["role"] != "maintainer" or artifact["os"] != "linux" or artifact["arch"] != arch:
            raise ValueError("maintenance component source/platform mismatch")
    if {p.name for p in root.iterdir()} - {"SHA256SUMS"} != expected:
        raise ValueError("incomplete or unexpected release assets")
    checksums = "".join(
        f"{hashlib.sha256((root / name).read_bytes()).hexdigest()}  {name}\n"
        for name in sorted(expected)
    )
    (root / "SHA256SUMS").write_text(checksums)
    expected.add("SHA256SUMS")
    ensure_tag(repo, tag, commit)
    # GraphQL resolves authorized drafts too; REST /releases/tags only resolves
    # published releases and cannot reconcile an interrupted first publication.
    release = lookup_release(repo, tag)
    if release is None:
        gh("release", "create", tag, "--repo", repo, "--target", commit,
           "--draft", "--prerelease", "--title", tag,
           "--notes", f"Complete Linux distributions built and checked from {commit}.")
        release = lookup_release(repo, tag)
    if release is None or (release["tagCommit"] or {}).get("oid") != commit:
        raise ValueError("release source differs from checked commit")
    assets = {a["name"] for a in release["releaseAssets"]["nodes"]}
    if assets - expected:
        raise ValueError("release contains unexpected assets")
    for name in sorted(expected):
        if name not in assets:
            if not release["isDraft"]:
                raise ValueError("published release is incomplete")
            gh("release", "upload", tag, str(root / name), "--repo", repo)
        with tempfile.TemporaryDirectory() as tmp:
            gh("release", "download", tag, "--repo", repo, "--pattern", name, "--dir", tmp)
            if (Path(tmp) / name).read_bytes() != (root / name).read_bytes():
                raise ValueError(f"published asset differs: {name}")
    if release["isDraft"]:
        gh("release", "edit", tag, "--repo", repo, "--draft=false", "--prerelease", "--latest=false")


if __name__ == "__main__":
    publish(Path(os.environ["RELEASE_ASSET_DIR"]), os.environ["GITHUB_SHA"], os.environ["GITHUB_REPOSITORY"])
