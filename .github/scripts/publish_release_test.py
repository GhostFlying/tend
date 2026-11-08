import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "publish_release", Path(__file__).with_name("publish-release.py")
)
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)


class PublishTest(unittest.TestCase):
    def test_first_publication_creates_tag_and_resolves_unpublished_draft(self):
        self.exercise(draft=True, first=True)

    def test_interrupted_draft_resumes_without_replacing_assets(self):
        self.exercise(draft=True)

    def test_published_release_is_verified_without_mutation(self):
        self.exercise(draft=False)

    def test_conflicting_asset_does_not_publish(self):
        with self.assertRaisesRegex(ValueError, "published asset differs"):
            self.exercise(draft=True, corrupt=True)

    def test_network_error_does_not_create_release(self):
        with tempfile.TemporaryDirectory() as directory:
            root, commit = self.fixture(directory)
            response = subprocess.CompletedProcess([], 1, "", "connection reset")
            with patch.object(publisher.subprocess, "run", return_value=response), patch.object(publisher, "gh") as gh:
                with self.assertRaisesRegex(RuntimeError, "connection reset"):
                    publisher.publish(root, commit, "example/tend")
                gh.assert_not_called()

    def fixture(self, directory):
        root = Path(directory)
        commit = "a" * 40
        for arch in ("amd64", "arm64", "fleet"):
            name = f"tend-main-{commit}-linux-{arch}"
            (root / (name + ".tar.gz")).write_bytes(b"complete distribution")
            (root / (name + ".json")).write_text(json.dumps({"commit": commit, "arch": "multi" if arch == "fleet" else arch}))
        return root, commit

    def exercise(self, draft, corrupt=False, first=False):
        with tempfile.TemporaryDirectory() as directory:
            root, commit = self.fixture(directory)
            remote = {p.name: p.read_bytes() for p in root.iterdir()}
            if draft:
                remote.pop(next(iter(remote)))
            else:
                import hashlib
                remote["SHA256SUMS"] = "".join(
                    f"{hashlib.sha256(remote[name]).hexdigest()}  {name}\n"
                    for name in sorted(remote)
                ).encode()
            if corrupt:
                remote[next(iter(remote))] = b"different bytes"
            if first:
                remote = {}
            release = {"isDraft": draft, "targetCommitish": commit, "releaseAssets": {"nodes": [{"name": name} for name in remote]}}
            mutations = []
            created = not first
            tag_created = not first

            def gh(*args):
                nonlocal created, tag_created
                if args[0] == "api":
                    if args[1] == "graphql":
                        return json.dumps({"data": {"repository": {"release": release if created else None}}})
                    if args[1].endswith("/git/refs"):
                        tag_created = True
                        return ""
                    self.assertTrue(tag_created)
                    return json.dumps({"object": {"type": "commit", "sha": commit}})
                if args[1] == "create":
                    self.assertTrue(tag_created)
                    self.assertFalse(created)
                    created = True
                elif args[1] == "download":
                    name = args[args.index("--pattern") + 1]
                    destination = Path(args[args.index("--dir") + 1])
                    (destination / name).write_bytes(remote[name])
                elif args[1] == "upload":
                    path = Path(args[3])
                    self.assertNotIn(path.name, remote)
                    remote[path.name] = path.read_bytes()
                    mutations.append("upload")
                elif args[1] == "edit":
                    self.assertEqual(set(remote), {p.name for p in root.iterdir()})
                    mutations.append("publish")
                else:
                    self.fail(f"unexpected command: {args}")
                return ""

            response = subprocess.CompletedProcess([], 1, "", "HTTP 404") if first else subprocess.CompletedProcess([], 0, json.dumps({"object": {"type": "commit", "sha": commit}}), "")
            with patch.object(publisher.subprocess, "run", return_value=response), patch.object(publisher, "gh", side_effect=gh):
                publisher.publish(root, commit, "example/tend")
            if draft:
                self.assertEqual(mutations[-1], "publish")
            else:
                self.assertEqual(mutations, [])


if __name__ == "__main__":
    unittest.main()
