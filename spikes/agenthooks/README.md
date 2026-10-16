# agenthooks spike

Checks how Claude Code, Codex and traex run command hooks and stdio MCP
servers (plan C5). The binary plays three roles:

- `probe hook <log> <label> <mode>` stands in for `tend hook`. It appends
  the hook input and a filtered environment to `<log>`, then answers with a
  unique `PROBE-...` codeword. Modes: `claude`/`codex` print
  `{"hookSpecificOutput":{"hookEventName":...,"additionalContext":...}}`,
  `plain` prints the codeword as plain text, `sleep` waits 30 s, `fail`
  exits 1, `block` exits 2, `garbage` prints invalid JSON, `bigN` prints
  about N thousand characters with a `MARK-<i>K` marker every thousand.
- `probe mcp <log> <label>` stands in for `tend mcp`: a minimal stdio MCP
  server with one tool, `probe_echo`, that logs every message and its
  environment.
- `probe run -bin codex|traex [-forward-env]` drives `<bin> app-server` the
  way tend-runner drives a worker, with the probe installed through `-c`
  hooks and MCP overrides and `TEND_ADDON=off` in the environment. It asks
  for the codewords, compacts with `thread/compact/start`, asks again, and
  writes `results.json`, `probe.jsonl` and a transcript.

The log path and label are passed on the command line, not through the
environment, because environment inheritance is one of the questions.

Build in the dev container, run on the host where the agents are logged in:

```sh
CGO_ENABLED=0 go build -o spikes/agenthooks/out/probe ./spikes/agenthooks
spikes/agenthooks/out/probe run -bin traex -forward-env
```

Run the agents from a scratch directory outside any repository, so no
AGENTS.md or CLAUDE.md is loaded. Unset `CLAUDECODE*` variables when
starting the agents from inside a Claude Code session, or the probe
records them as inherited. Interactive-mode checks use the agents'
non-interactive entry points:

```sh
P=$PWD/spikes/agenthooks/out/probe L=/tmp/probe.jsonl

# Claude Code: hooks through --settings, MCP through --mcp-config.
cat > /tmp/settings.json <<EOF
{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"$P hook $L claude claude"}]}],
"UserPromptSubmit":[{"hooks":[{"type":"command","command":"$P hook $L claude claude","timeout":3}]}]}}
EOF
echo "{\"mcpServers\":{\"probe\":{\"command\":\"$P\",\"args\":[\"mcp\",\"$L\",\"claude\"]}}}" > /tmp/mcp.json
TEND_ADDON=off claude -p "<prompt>" --settings /tmp/settings.json --setting-sources project \
  --mcp-config /tmp/mcp.json --strict-mcp-config --allowedTools mcp__probe__probe_echo \
  --output-format json --session-id <uuid> < /dev/null
claude -p "/compact" --resume <uuid> ...   # real compaction in print mode

# Codex and traex: hooks and MCP through -c overrides.
H="[{hooks=[{type=\"command\",command=\"$P hook $L codex codex\",timeout=3}]}]"
TEND_ADDON=off codex exec --skip-git-repo-check --ignore-user-config \
  --dangerously-bypass-hook-trust --json -o /tmp/last.txt \
  -c "hooks.SessionStart=$H" -c "hooks.UserPromptSubmit=$H" \
  -c "mcp_servers.probe.command=\"$P\"" -c "mcp_servers.probe.args=[\"mcp\",\"$L\",\"codex\"]" \
  -c 'mcp_servers.probe.default_tools_approval_mode="approve"' "<prompt>" < /dev/null
```

Ask the model for the `PROBE-` codewords it was given rather than to
"list lines in its context"; Codex models refuse the latter as disclosing
hidden instructions. Runtime output belongs in `spikes/agenthooks/out/`,
which is not committed. Conclusions are recorded in
`docs/findings/agent-hooks.md`.
