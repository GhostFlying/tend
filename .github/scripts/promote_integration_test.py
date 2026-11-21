import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "promote_integration", Path(__file__).with_name("promote-integration.py")
)
promotion = importlib.util.module_from_spec(spec)
spec.loader.exec_module(promotion)


class MainRouteTest(unittest.TestCase):
    def setUp(self):
        self.repo = "example/tend"
        self.head = "a" * 40
        self.url = "https://github.com/example/tend/actions/runs/1"
        self.pull = {
            "number": 12, "state": "open", "base": {"ref": "main"}, "labels": [],
            "head": {"sha": self.head, "ref": "fix/example", "repo": {"full_name": self.repo}},
        }
        self.pulls = self.enterContext(patch.object(promotion, "list_pulls", return_value=[self.pull]))
        self.api = self.enterContext(patch.object(promotion, "api", return_value=self.pull))

    def assert_route(self, state):
        promotion.publish_main_route(self.repo, self.head, self.url)
        args = self.api.call_args.args
        self.assertEqual(args[:4], (self.repo, f"statuses/{self.head}", "--method", "POST"))
        self.assertIn("context=main-route", args)
        self.assertIn(f"state={state}", args)

    def test_default_deny_and_live_label_addition_and_removal(self):
        self.assert_route("failure")
        self.pull["labels"] = [{"name": promotion.EXCEPTION_LABEL}]
        self.assert_route("success")
        self.pull["labels"] = []
        self.assert_route("failure")

    def test_only_repository_promotion_route_is_exempt(self):
        self.pull["head"]["ref"] = promotion.BRANCH
        self.assert_route("success")
        self.pull["head"]["repo"]["full_name"] = "fork/tend"
        self.assert_route("failure")

    def test_shared_head_cannot_borrow_another_prs_label(self):
        self.pull["labels"] = [{"name": promotion.EXCEPTION_LABEL}]
        other_pull = {**self.pull, "number": 13, "labels": []}
        self.pulls.return_value = [self.pull, other_pull]
        self.assert_route("failure")
        other_pull["head"] = {**self.pull["head"], "sha": "b" * 40}
        self.assert_route("success")

    def test_publication_targets_the_latest_pr_head(self):
        self.pull["labels"] = [{"name": promotion.EXCEPTION_LABEL}]
        self.assert_route("success")
        self.pull["head"] = {**self.pull["head"], "sha": "b" * 40}
        self.assert_route("failure")
        self.head = self.pull["head"]["sha"]
        self.assert_route("success")

    def test_closing_the_last_main_pr_revokes_the_status(self):
        self.pull["labels"] = [{"name": promotion.EXCEPTION_LABEL}]
        self.assert_route("success")
        self.pulls.return_value = []
        self.assert_route("failure")


class PromotionTest(unittest.TestCase):
    def setUp(self):
        folder = self.enterContext(tempfile.TemporaryDirectory())
        self.addCleanup(os.chdir, Path.cwd())
        os.chdir(folder)
        self.repo = "example/tend"
        promotion.git("init", "--initial-branch=main")
        promotion.git("config", "user.name", "Git fixture")
        promotion.git("config", "user.email", "fixture@example.invalid")
        self.commit("base.txt", "base")
        self.base = self.commit(".github/fixture", "workflow setup")
        promotion.git("branch", "integration")
        promotion.git("init", "--bare", "--initial-branch=main", "remote.git")
        promotion.git("remote", "add", "origin", str(Path("remote.git").resolve()))
        self.results = {}
        self.main_prs = []
        self.enterContext(patch.object(promotion, "list_pulls", side_effect=self.list_pulls))
        self.enterContext(patch.object(promotion, "api", side_effect=self.api))

    def list_pulls(self, repo, base, state, head=None):
        self.assertEqual(repo, self.repo)
        self.assertEqual(base, "main")
        return [pr for pr in self.main_prs if pr.get("state", "open") == state]

    def api(self, repo, path, *options):
        self.assertEqual(repo, self.repo)
        return self.results[path]

    def commit(self, path, data):
        file = Path(path)
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(data, encoding="utf-8")
        promotion.git("add", path)
        promotion.git("commit", "-m", f"change {path}")
        return promotion.git("rev-parse", "HEAD")

    def admit(self, number, *files):
        promotion.git("switch", "integration")
        landed = [self.commit(file, f"integration PR {number}\n") for file in files]
        pr = {"number": number, "commits": len(files), "merge_commit_sha": landed[-1],
              "base": {"ref": "integration"},
              "merged_at": "2026-11-21T00:00:00Z"}
        for sha in landed:
            self.results[f"commits/{sha}/pulls"] = [[pr]]
        promotion.git("switch", "main")
        return pr

    def candidate(self, main, head, previous, prs, old=""):
        promotion.create_candidate(self.repo, main, head, previous, prs, old)
        return promotion.fetch_branch(promotion.BRANCH)

    def land(self, number, base, head):
        # Model GitHub rewriting commits and retaining the final tree and PR head.
        env = dict(os.environ, GIT_COMMITTER_DATE="2000-01-01T00:00:00Z")
        promotion.git("cherry-pick", f"{base}..{head}", env=env)
        landed = promotion.git("rev-parse", "HEAD")
        self.assertNotEqual(landed, head)
        pr = {"number": number, "state": "closed", "base": {"ref": "main"},
              "merged_at": "2026-11-21T00:00:00Z",
              "merge_commit_sha": landed, "head": {"sha": head, "ref": promotion.BRANCH,
                                                   "repo": {"full_name": self.repo}}}
        self.results[f"git/commits/{head}"] = {"tree": {"sha": promotion.tree(head)}}
        self.results[f"pulls/{number}"] = pr
        self.main_prs.append(pr)
        return landed

    def test_two_batches_replay_only_unpromoted_changes(self):
        a = self.admit(1, "one", "two")
        b = self.admit(2, "three")
        c = self.admit(3, "four")
        d = self.admit(4, "five")
        tip = d["merge_commit_sha"]
        prs = promotion.source_pulls(self.repo, self.base, tip)
        self.assertEqual(prs, [a, b, c])
        head = self.candidate(self.base, tip, self.base, prs)
        first_main = self.land(5, self.base, head)
        previous = promotion.previous_cutoff(self.repo, first_main, tip)
        self.assertEqual(previous, c["merge_commit_sha"])
        promotion.git("push", "origin", "main", "integration")
        with patch.object(promotion, "open_promotion") as open_pr:
            promotion.prepare(self.repo, argparse.Namespace(replace=None, through=None, reconcile=False))
        second = promotion.fetch_branch(promotion.BRANCH)
        open_pr.assert_called_once()
        self.assertEqual(promotion.receipt_at(second)["pull_requests"], [promotion.record(d)])
        self.assertEqual(promotion.git("diff", "--name-only", first_main, second),
                         f"{promotion.RECEIPT}\nfive")

    def test_replacement_keeps_original_prs_and_includes_fix(self):
        original = self.admit(1, "original")
        head = self.candidate(self.base, original["merge_commit_sha"], self.base, [original])
        fix = self.admit(2, "fix")
        promotion.git("push", "origin", "main", "integration")
        prepared = {"number": 9, "base": {"ref": "main"}, "html_url": "http://example.invalid",
                    "head": {"sha": head, "ref": promotion.BRANCH, "repo": {"full_name": self.repo}}}
        self.main_prs.append(prepared)
        promotion.prepare(self.repo, argparse.Namespace(replace=9, through=2, reconcile=False))
        new_head = promotion.fetch_branch(promotion.BRANCH)
        receipt = promotion.receipt_at(new_head)
        self.assertEqual(receipt["pull_requests"], [promotion.record(original), promotion.record(fix)])

    def test_rejects_wrong_range_and_wrong_test_tree(self):
        a = self.admit(1, "one")
        b = self.admit(2, "two")
        head = self.candidate(self.base, b["merge_commit_sha"], self.base, [a, b])
        with self.assertRaisesRegex(ValueError, "different tree"):
            promotion.verify_candidate(self.repo, self.base, b["merge_commit_sha"], head,
                                       promotion.tree(self.base))
        promotion.git("switch", "--detach", head)
        receipt = promotion.receipt_at(head)
        receipt["pull_requests"] = [promotion.record(b)]
        Path(promotion.RECEIPT).write_text(json.dumps(receipt), encoding="utf-8")
        promotion.git("commit", "-am", "forge incomplete source range", "--amend")
        with self.assertRaisesRegex(ValueError, "do not cover"):
            promotion.verify_candidate(self.repo, self.base, b["merge_commit_sha"], "HEAD",
                                       promotion.tree("HEAD"))

    def test_source_selection_uses_only_commits_that_landed_after_rebase(self):
        pr = self.admit(1, "one")
        pr["commits"] = 3  # Original PR: one empty commit, one already applied.
        tip = pr["merge_commit_sha"]
        self.results[f"commits/{tip}/pulls"][0].append({
            "number": 2, "base": {"ref": "integration"}, "merged_at": None,
        })
        self.assertEqual(promotion.source_pulls(self.repo, self.base, tip), [pr])
        self.results[f"commits/{tip}/pulls"] = [[]]
        with self.assertRaisesRegex(ValueError, "cannot establish"):
            promotion.source_pulls(self.repo, self.base, tip)

    def test_conflict_preserves_source_and_does_not_push(self):
        a = self.admit(1, "base.txt")
        main = self.commit("base.txt", "conflicting main change")
        with self.assertRaises(subprocess.CalledProcessError):
            self.candidate(main, a["merge_commit_sha"], self.base, [a])
        self.assertEqual(promotion.git("rev-parse", "HEAD"), main)
        self.assertFalse(promotion.git("ls-remote", "origin", f"refs/heads/{promotion.BRANCH}"))

    def test_verification_matches_cherry_pick_across_main_rename(self):
        promotion.git("mv", "base.txt", "renamed.txt")
        promotion.git("commit", "-m", "rename main file")
        main = promotion.git("rev-parse", "HEAD")
        a = self.admit(1, "base.txt")
        head = self.candidate(main, a["merge_commit_sha"], self.base, [a])
        self.assertEqual(promotion.git("show", f"{head}:renamed.txt"), "integration PR 1")

    def test_reconciled_range_recovers_diverged_main_after_integration_repair(self):
        a = self.admit(1, "base.txt", "feature.txt")
        main = self.commit("base.txt", "main emergency correction")
        b = self.admit(2, "recovery-note.txt")
        promotion.git("switch", "integration")
        b["merge_commit_sha"] = self.commit("base.txt", "main emergency correction")
        b["commits"] += 1
        tip = b["merge_commit_sha"]
        self.results[f"commits/{tip}/pulls"] = [[b]]
        promotion.git("switch", "main")
        with self.assertRaises(subprocess.CalledProcessError):
            self.candidate(main, tip, self.base, [a, b])
        promotion.create_candidate(self.repo, main, tip, self.base, [a, b], "", reconcile=True)
        head = promotion.fetch_branch(promotion.BRANCH)
        receipt = promotion.verify_candidate(self.repo, main, tip, head, promotion.tree(head))
        self.assertEqual(receipt["pull_requests"], [promotion.record(a), promotion.record(b)])
        self.assertEqual(promotion.git("show", f"{head}:base.txt"), "main emergency correction")
        self.assertEqual(promotion.git("show", f"{head}:feature.txt"), "integration PR 1")

    def test_reconciled_range_accepts_deletion_already_applied_on_main(self):
        a = self.admit(1, "base.txt", "feature.txt")
        promotion.git("rm", "base.txt")
        promotion.git("commit", "-m", "remove obsolete file on main")
        main = promotion.git("rev-parse", "HEAD")
        b = self.admit(2, "recovery-note.txt")
        promotion.git("switch", "integration")
        promotion.git("rm", "base.txt")
        promotion.git("commit", "-m", "remove obsolete file on integration")
        b["merge_commit_sha"] = promotion.git("rev-parse", "HEAD")
        b["commits"] += 1
        tip = b["merge_commit_sha"]
        self.results[f"commits/{tip}/pulls"] = [[b]]
        promotion.git("switch", "main")
        promotion.create_candidate(self.repo, main, tip, self.base, [a, b], "", reconcile=True)
        head = promotion.fetch_branch(promotion.BRANCH)
        self.assertFalse(promotion.git("ls-tree", "--name-only", head, "--", "base.txt"))
        self.assertEqual(promotion.git("show", f"{head}:feature.txt"), "integration PR 1")

    def test_reconciliation_uses_promoted_cutoff_after_main_rebase(self):
        first = self.admit(1, "base.txt")
        cutoff = first["merge_commit_sha"]
        head = self.candidate(self.base, cutoff, self.base, [first])
        main = self.land(3, self.base, head)
        self.assertFalse(promotion.ancestor(cutoff, main))
        second = self.admit(2, "base.txt")
        main = self.commit("unrelated-hotfix.txt", "main only")
        tip = second["merge_commit_sha"]
        promotion.create_candidate(self.repo, main, tip, cutoff, [second], head, reconcile=True)
        recovered = promotion.fetch_branch(promotion.BRANCH)
        self.assertEqual(promotion.git("show", f"{recovered}:base.txt"), "integration PR 2")
        self.assertEqual(promotion.git("show", f"{recovered}:unrelated-hotfix.txt"), "main only")

    def test_rejects_unknown_or_mismatched_landed_promotion(self):
        a = self.admit(1, "one")
        tip = a["merge_commit_sha"]
        head = self.candidate(self.base, tip, self.base, [a])
        landed = self.land(2, self.base, head)
        del self.main_prs[:]
        with self.assertRaisesRegex(ValueError, "cannot establish"):
            promotion.previous_cutoff(self.repo, landed, tip)
        self.main_prs.append(self.results["pulls/2"])
        self.results[f"git/commits/{head}"]["tree"]["sha"] = promotion.tree(self.base)
        with self.assertRaisesRegex(ValueError, "landed tree differs"):
            promotion.previous_cutoff(self.repo, landed, tip)
