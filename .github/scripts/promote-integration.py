"""Assemble, verify and merge the repository's continuous integration batches."""

import argparse
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


BRANCH = "promote/integration"
RECEIPT = ".github/integration-batch.json"
EXCEPTION_LABEL = "skip_integration"
ROUTE_STATUS = "main-route"
MAX_PRS = 3
MAX_WAIT = timedelta(minutes=15)


def run(*args, **options):
    return subprocess.check_output(args, text=True, **options).strip()


def git(*args, **options):
    return run("git", *args, **options)


def api(repo, path, *options):
    return json.loads(run("gh", "api", f"repos/{repo}/{path}", *options))


def list_pulls(repo, base, state, head=None):
    args = ["--method", "GET", "--paginate", "--slurp",
            "-f", f"base={base}", "-f", f"state={state}", "-F", "per_page=100"]
    if head:
        args.extend(("-f", f"head={repo.split('/')[0]}:{head}"))
    return [pr for page in api(repo, "pulls", *args) for pr in page]


def source_pulls(repo, base, head, through=None):
    # Attribute the commits that actually landed. GitHub can omit commits from
    # the original branch when rebasing it onto a target that already has them.
    selected = []
    pending = None
    for sha in commits(base, head):
        pages = api(repo, f"commits/{sha}/pulls", "--paginate", "--slurp")
        matches = [pr for page in pages for pr in page
                   if pr["merged_at"] and pr["base"]["ref"] == "integration"]
        require(len(matches) == 1, f"cannot establish the integration PR for {sha}")
        pr = matches[0]
        require(pending is None or pending == pr["number"],
                f"integration range changed PR before reaching the merge of #{pending}")
        if sha != pr["merge_commit_sha"]:
            pending = pr["number"]
            continue
        pending = None
        selected.append(pr)
        if pr["number"] == through or (through is None and len(selected) == MAX_PRS):
            return selected
    require(through is None, f"PR #{through} is outside the pending integration range")
    require(pending is None, f"integration range stops before the merge of PR #{pending}")
    return selected


def require(condition, message):
    if not condition:
        raise ValueError(message)


def ancestor(base, head):
    result = subprocess.run(("git", "merge-base", "--is-ancestor", base, head), check=False)
    if result.returncode not in (0, 1):
        result.check_returncode()
    return result.returncode == 0


def commits(base, head):
    require(ancestor(base, head), f"{base} is outside the ancestry of {head}")
    require(not git("rev-list", "--merges", f"{base}..{head}"), "expected linear history")
    return git("rev-list", "--reverse", f"{base}..{head}").splitlines()


def tree(commit):
    return git("rev-parse", f"{commit}^{{tree}}")


def receipt_at(commit):
    if git("ls-tree", "--name-only", commit, "--", RECEIPT) != RECEIPT:
        return None
    data = json.loads(git("show", f"{commit}:{RECEIPT}"))
    require(data.get("version") == 1, "unknown integration receipt version")
    return data


def record(pr):
    return {"number": pr["number"], "merge_sha": pr["merge_commit_sha"]}


def is_promotion(repo, pull):
    head = pull["head"]
    return (pull["base"]["ref"] == "main" and head["ref"] == BRANCH
            and bool(head["repo"]) and head["repo"]["full_name"] == repo)


def publish_main_route(repo, head, run_url):
    require(re.fullmatch(r"[0-9a-f]{40}", head), "expected a full GitHub head SHA")
    # The status belongs to a commit. Read all active main PRs for that
    # commit so any event in its queue can reconcile the whole status.
    matches = [pr for pr in list_pulls(repo, "main", "open") if pr["head"]["sha"] == head]
    status = "failure"
    if not matches:
        description = "No open main pull request for this head."
    elif len(matches) != 1:
        description = "Main route requires a head unique to this open main PR."
    elif is_promotion(repo, matches[0]):
        status = "success"
        description = "Promotion route. Full CI verifies the receipt."
    elif any(label["name"] == EXCEPTION_LABEL for label in matches[0]["labels"]):
        status = "success"
        description = f"Exception route with {EXCEPTION_LABEL}. Full CI is still required."
    else:
        description = f"Target integration or obtain authorization for {EXCEPTION_LABEL}."

    api(repo, f"statuses/{head}", "--method", "POST",
        "-f", f"context={ROUTE_STATUS}", "-f", f"state={status}",
        "-f", f"description={description}", "-f", f"target_url={run_url}")
    print(f"{head} {ROUTE_STATUS}: {status}. {description}")


def merged_receipt(repo, main, pull):
    require(is_promotion(repo, pull) and pull["merged_at"], "expected a merged promotion PR")
    landed = pull["merge_commit_sha"]
    require(ancestor(landed, main), "promotion merge is absent from main")
    passed = api(repo, f"git/commits/{pull['head']['sha']}")["tree"]["sha"]
    require(passed == tree(landed), "landed tree differs from the promotion PR head")
    receipt = receipt_at(landed)
    require(receipt, "landed promotion has no receipt")
    return receipt


def previous_cutoff(repo, main, integration):
    if receipt_at(main) is None:
        require(not git("log", "-1", "--format=%H", main, "--", RECEIPT),
                "main history has a promotion receipt but the current tree lost it")
        return git("merge-base", main, integration)
    landed = git("log", "-1", "--format=%H", main, "--", RECEIPT)
    matching = [pull for pull in list_pulls(repo, "main", "closed", BRANCH)
                if pull["merged_at"] and pull["merge_commit_sha"] == landed]
    require(len(matching) == 1, "cannot establish the last promotion PR from main")
    return merged_receipt(repo, main, matching[0])["integration_through"]


@contextmanager
def detached_workspace(base):
    with tempfile.TemporaryDirectory(prefix="promotion-") as directory:
        workspace = Path(directory) / "tree"
        git("worktree", "add", "--detach", str(workspace), base)
        try:
            yield workspace
        finally:
            git("worktree", "remove", "--force", str(workspace))


def replay_tree(base, sources, *, reconciliation=None):
    if reconciliation is None:
        # Use the same merge strategy as preparation, including rename
        # detection, without creating commits in the validation worktree.
        with detached_workspace(base) as workspace:
            git("cherry-pick", "--no-commit", *sources, cwd=workspace)
            return git("write-tree", cwd=workspace)

    previous, cutoff = reconciliation
    # On Git 2.39, merge-tree cannot take an explicit merge base. Give this
    # subprocess alone the intended ancestry. Main and the integration tip are
    # two changes relative to the last promoted integration cutoff.
    with tempfile.NamedTemporaryFile(mode="w", prefix="promotion-ancestry-") as ancestry:
        for tip in sorted({base, cutoff} - {previous}):
            ancestry.write(f"{tip} {previous}\n")
        ancestry.flush()
        env = dict(os.environ, GIT_GRAFT_FILE=ancestry.name)
        result = git("-c", "advice.graftFileDeprecated=false", "merge-tree", "--write-tree",
                     base, cutoff, env=env)
        return result.splitlines()[0]


def verify_candidate(repo, main, integration, head, tested_tree):
    receipt = receipt_at(head)
    require(receipt and receipt["main_base"] == main, "promotion was not prepared on this main base")
    previous = previous_cutoff(repo, main, integration)
    require(receipt["integration_from"] == previous, "promotion starts at the wrong cutoff")
    cutoff = receipt["integration_through"]
    require(ancestor(cutoff, integration), "promotion cutoff is outside integration")
    prs = receipt["pull_requests"]
    require(prs, "promotion has no source PRs")
    observed = source_pulls(repo, previous, cutoff, through=prs[-1]["number"])
    require([record(pr) for pr in observed] == prs and prs[-1]["merge_sha"] == cutoff,
            "source PRs do not cover the recorded integration range")
    sources = commits(previous, cutoff)
    mode = receipt.get("replay", "commits")
    require(mode in ("commits", "range"), "unknown promotion replay mode")
    reconciled = (previous, cutoff) if mode == "range" else None
    expected_tree = replay_tree(main, sources, reconciliation=reconciled)
    replay_count = int(expected_tree != tree(main)) if reconciled else len(sources)
    require(len(commits(main, head)) == replay_count + 1, "promotion commit count differs from the source")
    require(git("diff", "--name-only", f"{head}^", head) == RECEIPT,
            "the final promotion commit must contain only the receipt")
    require(tree(f"{head}^") == expected_tree, "promotion content differs from the source replay")
    require(tree(head) == tested_tree, "GitHub is testing a different tree from the promotion head")
    return receipt


def fetch_branch(branch):
    ref = f"refs/remotes/origin/{branch}"
    git("fetch", "--no-tags", "origin", f"+refs/heads/{branch}:{ref}")
    return git("rev-parse", ref)


def verify_event(event, repo):
    pull = event["pull_request"]
    main, head = pull["base"]["sha"], pull["head"]["sha"]
    changed_receipt = git("diff", "--name-only", main, "HEAD", "--", RECEIPT)
    if is_promotion(repo, pull):
        verify_candidate(repo, main, fetch_branch("integration"), head, tree("HEAD"))
    else:
        require(not changed_receipt, "only an integration promotion may change the receipt")


def create_candidate(repo, main, integration, previous, prs, old_head, *, reconcile=False):
    cutoff = prs[-1]["merge_commit_sha"]
    receipt = {
        "version": 1,
        "main_base": main,
        "integration_from": previous,
        "integration_through": cutoff,
        "pull_requests": [record(pr) for pr in prs],
    }
    sources = commits(previous, cutoff)
    if reconcile:
        receipt["replay"] = "range"
        reconciled_tree = replay_tree(main, sources, reconciliation=(previous, cutoff))
    with detached_workspace(main) as workspace:
        if not reconcile:
            git("cherry-pick", *sources, cwd=workspace)
        elif reconciled_tree != tree(main):
            git("read-tree", "--reset", "-u", reconciled_tree, cwd=workspace)
            git("commit", "-m", "fix: reconcile integration with main", cwd=workspace)
        (workspace / RECEIPT).write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        git("add", RECEIPT, cwd=workspace)
        git("commit", "-m", "chore: record integration promotion", cwd=workspace)
        head = git("rev-parse", "HEAD", cwd=workspace)
        verify_candidate(repo, main, integration, head, tree(head))
        ref = f"refs/heads/{BRANCH}"
        git("push", f"--force-with-lease={ref}:{old_head}", "origin", f"{head}:{ref}")
    return receipt


def open_promotion(repo, receipt):
    numbers = ", ".join(f"#{pr['number']}" for pr in receipt["pull_requests"])
    print(run("gh", "pr", "create", "--repo", repo, "--base", "main", "--head", BRANCH,
              "--title", "chore: promote integration batch", "--body",
              f"Promote {numbers} through {receipt['integration_through']}.\n\n"
              f"The source range and main baseline are recorded in {RECEIPT}."))


def prepare(repo, args):
    main = fetch_branch("main")
    integration = fetch_branch("integration")
    active = list_pulls(repo, "main", "open", BRANCH)
    require(len(active) <= 1 and all(is_promotion(repo, pr) for pr in active),
            "expected at most one open promotion from this repository")
    old_head = ""
    through = args.through
    reconcile = args.reconcile
    if active:
        pull = active[0]
        if args.replace is None:
            print(f"Active promotion: {pull['html_url']}")
            return
        require(args.replace == pull["number"], "replacement PR is not the active promotion")
        old_head = fetch_branch(BRANCH)
        require(old_head == pull["head"]["sha"], "promotion head changed during preparation")
        frozen = receipt_at(old_head)
        require(frozen, "active promotion has no receipt")
        reconcile = reconcile or frozen.get("replay") == "range"
        through = through or frozen["pull_requests"][-1]["number"]
    else:
        require(args.replace is None, "no open promotion to replace")
        require((through is not None) == reconcile, "new reconciliation requires --reconcile and --through together")
        remote = git("ls-remote", "--refs", "origin", f"refs/heads/{BRANCH}")
        if remote:
            orphan = fetch_branch(BRANCH)
            closed = [pr for pr in list_pulls(repo, "main", "closed", BRANCH)
                      if pr["head"]["sha"] == orphan]
            if closed:
                merged = [pr for pr in closed if pr["merged_at"]]
                require(len(merged) == 1,
                        "promotion branch belongs to a closed PR without a confirmed merge")
                merged_receipt(repo, main, merged[0])
                old_head = orphan
            else:
                receipt = receipt_at(orphan)
                require(receipt, "existing promotion branch has no receipt")
                verify_candidate(repo, receipt["main_base"], integration, orphan, tree(orphan))
                open_promotion(repo, receipt)
                return

    previous = previous_cutoff(repo, main, integration)
    prs = source_pulls(repo, previous, integration, through=through)
    if not prs:
        print("No integration PRs are waiting for promotion.")
        return
    if active:
        require(ancestor(frozen["integration_through"], prs[-1]["merge_commit_sha"]),
                "replacement cannot drop PRs from the frozen batch")
        if (frozen["main_base"] == main and frozen["integration_through"] == prs[-1]["merge_commit_sha"]
                and reconcile == (frozen.get("replay") == "range")):
            print(f"Active promotion already matches the requested range: {pull['html_url']}")
            return
    elif not reconcile:
        age = datetime.now(timezone.utc) - datetime.fromisoformat(prs[0]["merged_at"])
        if len(prs) < MAX_PRS and age < MAX_WAIT:
            minutes = int(age.total_seconds() // 60)
            print(f"Waiting to fill batch ({len(prs)}/{MAX_PRS} PRs, {minutes} minutes old).")
            return

    receipt = create_candidate(repo, main, integration, previous, prs, old_head, reconcile=reconcile)
    if active:
        print(f"Refreshed {pull['html_url']}. Wait for full CI on the new head.")
    else:
        open_promotion(repo, receipt)


def merge(repo, number):
    pull = api(repo, f"pulls/{number}")
    require(is_promotion(repo, pull), "expected the repository's promotion PR")
    if not pull["merged_at"]:
        require(pull["state"] == "open", "promotion PR is closed without a merge")
        head = fetch_branch(BRANCH)
        require(head == pull["head"]["sha"], "promotion head changed before merge")
        base = fetch_branch("main")
        verify_candidate(repo, base, fetch_branch("integration"), head, tree(head))
        print(run("gh", "pr", "merge", str(number), "--repo", repo, "--rebase",
                  "--match-head-commit", head))
        pull = api(repo, f"pulls/{number}")
    main = fetch_branch("main")
    receipt = merged_receipt(repo, main, pull)
    print(f"Promoted through {receipt['integration_through']} at main commit {pull['merge_commit_sha']}.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    stage = commands.add_parser("prepare", help="prepare or reconcile the active promotion")
    stage.add_argument("--replace", type=int, metavar="PR")
    stage.add_argument("--through", type=int, metavar="INTEGRATION_PR")
    stage.add_argument("--reconcile", action="store_true", help="replay the repaired range as a single delta")
    finish = commands.add_parser("merge", help="merge a checked promotion or reconcile its receipt")
    finish.add_argument("number", type=int)
    commands.add_parser("verify", help="verify the GitHub pull request event")
    route = commands.add_parser("main-route", help="publish the main route status for an open pull request")
    route.add_argument("head")
    route.add_argument("run_url")
    args = parser.parse_args()
    try:
        if args.command == "verify":
            with open(os.environ["GITHUB_EVENT_PATH"], encoding="utf-8") as stream:
                verify_event(json.load(stream), os.environ["GITHUB_REPOSITORY"])
        elif args.command == "main-route":
            publish_main_route(os.environ["GITHUB_REPOSITORY"], args.head, args.run_url)
        else:
            origin = git("remote", "get-url", "origin")
            require(origin == git("remote", "get-url", "--push", "origin"),
                    "origin must fetch and push to the same GitHub repository")
            repo = run("gh", "repo", "view", origin, "--json", "nameWithOwner", "--jq", ".nameWithOwner")
            if args.command == "prepare":
                prepare(repo, args)
            else:
                merge(repo, args.number)
    except subprocess.CalledProcessError as err:
        details = err.stdout.strip() if err.stdout else str(err)
        parser.exit(1, f"promotion: {details}\n")
    except (ValueError, OSError, KeyError) as err:
        parser.exit(1, f"promotion: {err}\n")


if __name__ == "__main__":
    main()
