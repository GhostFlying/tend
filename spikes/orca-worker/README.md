# Spike: agent TUI workers in orca (D63)

Question: can tend run a single-repository task as an agent TUI in an
orca worktree, deliver its result through tend, and notice a human taking
over? The answers feed the phase E plan.

Runs against an orca host the owner approved for the spike, with a local
scratch repository that has no remote. Every orca object the spike makes
is named `tend-spike-*` and removed at the end (see Cleanup).

## Checks

1. Workspace: `orca worktree create --repo <scratch> --name tend-spike-1
   --json`. What comes back (id, path, branch, base) and whether it maps to
   tend's workspace facts (resolved base sha, branch).
2. Launch: `orca agent launch --agent <codex|claude|traex> --worktree ...
   --prompt <brief> --json`, as a terminal, not a chat. The handle, the
   launch receipt and `--retry-launch` replay. Which launch keeps each agent
   from stopping at a permission prompt. Whether traex is a known agent id.
3. Delivery: the agent in the TUI calls an MCP tool (a stub standing in
   for tend's worker mode) to submit its result. How the run's token
   reaches it: orca's per-workspace env, or a file in the worktree.
4. Turn state: `terminal wait --for tui-idle`, agent status, and the send
   receipt's `turn_started`. Whether a turn the owner starts in orca can
   be told apart from one tend started.
5. Restart: what happens to handles and the agent session when the orca
   server restarts, and how a run would continue its session.

## Cleanup

Close the spike's terminals, remove its worktrees and branches, remove the
scratch repository from orca and from disk, and list orca's repos and
worktrees to confirm nothing named `tend-spike-*` is left.

Findings go to `docs/findings/orca-worker.md`; outputs of the spike are
not committed.
