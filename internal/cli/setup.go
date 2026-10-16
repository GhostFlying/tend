package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// hookTimeout is the agents' timeout for the hook command, in seconds:
// longer than the command's own, so it gives up and answers first.
const hookTimeout = 5

// runSetup shows, and with --write applies, an agent's configuration for
// the add-on (C5 findings). Nothing is changed without --write.
func runSetup(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "setup")
	write := fs.Bool("write", false, "change the agent's configuration files")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !protocol.AgentKind(pos[0]).Valid() {
		return usagef("setup takes one agent: claude, codex or traex")
	}
	agent := protocol.AgentKind(pos[0])
	s := &setup{e: e, agent: agent, tend: e.tendCommand(), write: *write}
	if agent == protocol.AgentClaude {
		err = s.claude(ctx)
	} else {
		err = s.codexLike()
	}
	if err != nil {
		return err
	}
	switch {
	case s.pending:
		fmt.Fprintln(e.Stdout, "\nNothing was changed. Run again with --write to apply.")
	case !*write:
		fmt.Fprintf(e.Stdout, "\nNothing to change: %s is already set up for tend.\n", agent)
	}
	return nil
}

type setup struct {
	e     *Env
	agent protocol.AgentKind
	tend  string
	write bool
	// pending is set once a change was shown but not written.
	pending bool
}

// setupEvents are the hooks tend installs, by the agents' event names.
var setupEvents = []struct{ name, event string }{{"SessionStart", "session-start"}, {"UserPromptSubmit", "user-prompt-submit"}}

// tendCommand names tend in agent configuration: plainly when PATH finds
// this binary, otherwise by its path. The name must stay the same across
// upgrades, since Codex and traex trust a hook by its command.
func (e *Env) tendCommand() string {
	if p, err := e.LookPath("tend"); err == nil {
		a, errA := os.Stat(p)
		b, errB := os.Stat(e.Executable)
		if errA == nil && errB == nil && os.SameFile(a, b) {
			return "tend"
		}
	}
	return e.Executable
}

// shellWord quotes a word for the shell the agents run hook commands in.
func shellWord(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_./:@%+=-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (s *setup) hookCommand(event string) string {
	return fmt.Sprintf("%s hook %s %s", shellWord(s.tend), s.agent, event)
}

// hookEntry is one command hook in the agents' common JSON shape.
func (s *setup) hookEntry(event string) *object {
	h := newObject()
	h.set("type", "command")
	h.set("command", s.hookCommand(event))
	h.set("timeout", hookTimeout)
	if s.agent != protocol.AgentClaude {
		// tend keeps the context within its own budget; Codex and traex
		// would otherwise spill anything over 2,500 tokens to a file.
		h.set("additionalContextLimit", 0)
	}
	return h
}

// addHooks adds tend's two hooks to the "hooks" object of root unless a
// hook with the same command is there.
func (s *setup) addHooks(root *object) error {
	hooks, err := root.child("hooks")
	if err != nil {
		return err
	}
	for _, ev := range setupEvents {
		groups, err := hooks.list(ev.name)
		if err != nil {
			return err
		}
		want := s.hookCommand(ev.event)
		found := false
		for _, g := range groups {
			if g, ok := g.(*object); ok {
				hs, _ := g.list("hooks")
				for _, h := range hs {
					if h, ok := h.(*object); ok && h.get("command") == want {
						found = true
					}
				}
			}
		}
		if !found {
			g := newObject()
			g.set("hooks", []any{s.hookEntry(ev.event)})
			hooks.set(ev.name, append(groups, g))
		}
	}
	return nil
}

// change shows how a file changes and writes it with --write.
func (s *setup) change(path, before, after string) error {
	if before == after {
		fmt.Fprintf(s.e.Stdout, "%s already has tend's configuration.\n", path)
		return nil
	}
	fmt.Fprintf(s.e.Stdout, "Changes to %s:\n%s", path, diff(before, after))
	if !s.write {
		s.pending = true
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	perm := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		perm = st.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(after), perm); err != nil {
		return err
	}
	fmt.Fprintf(s.e.Stdout, "Wrote %s.\n", path)
	return nil
}

func readOptional(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// editJSON applies edit to a JSON file, keeping what is there.
func (s *setup) editJSON(path string, edit func(*object) error) error {
	before, err := readOptional(path)
	if err != nil {
		return err
	}
	root, err := parseOrdered([]byte(before))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := edit(root); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	after := formatOrdered(root)
	if strings.TrimSpace(before) != "" && sameJSON(before, after) {
		after = before
	}
	return s.change(path, before, after)
}

// sameJSON reports whether two documents differ only in layout.
func sameJSON(a, b string) bool {
	x, err1 := parseOrdered([]byte(a))
	y, err2 := parseOrdered([]byte(b))
	return err1 == nil && err2 == nil && formatOrdered(x) == formatOrdered(y)
}

// claude configures Claude Code: hooks and the tool permission in its user
// settings, and the MCP server through `claude mcp add`.
func (s *setup) claude(ctx context.Context) error {
	dir := s.e.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(s.e.Home, ".claude")
	}
	err := s.editJSON(filepath.Join(dir, "settings.json"), func(root *object) error {
		if err := s.addHooks(root); err != nil {
			return err
		}
		perms, err := root.child("permissions")
		if err != nil {
			return err
		}
		allow, err := perms.list("allow")
		if err != nil {
			return err
		}
		for _, a := range allow {
			if a == "mcp__tend__*" {
				return nil
			}
		}
		perms.set("allow", append(allow, "mcp__tend__*"))
		return nil
	})
	if err != nil {
		return err
	}
	add := []string{"mcp", "add", "--scope", "user", "tend", "--", s.tend, "mcp", "--agent", "claude"}
	if s.e.Run(ctx, "claude", "mcp", "get", "tend") == nil {
		fmt.Fprintf(s.e.Stdout, "\nClaude Code already has an MCP server named tend; check that it runs:\n  %s mcp --agent claude\n", s.tend)
		return nil
	}
	fmt.Fprintf(s.e.Stdout, "\nThe MCP server is added with:\n  claude %s\n", strings.Join(add, " "))
	if !s.write {
		s.pending = true
		return nil
	}
	if err := s.e.Run(ctx, "claude", add...); err != nil {
		return fmt.Errorf("claude mcp add: %w", err)
	}
	fmt.Fprintln(s.e.Stdout, "Added the MCP server.")
	return nil
}

var mcpSection = regexp.MustCompile(`(?m)^\s*\[\s*mcp_servers\s*\.\s*"?tend"?\s*\]`)

var (
	tomlHeader  = regexp.MustCompile(`^\s*(\[\[?)\s*([^\]]*?)\s*\]\]?\s*(#.*)?$`)
	tomlKey     = regexp.MustCompile(`^\s*([A-Za-z0-9_\-."' ]+?)\s*=`)
	tomlCommand = regexp.MustCompile(`(?m)^\s*command\s*=\s*("(?:[^"\\]|\\.)*"|'[^']*')`)
)

// tomlName normalizes a TOML key or table name: no spaces around dots and
// no quotes, which is enough to compare the names setup cares about.
func tomlName(s string) string {
	parts := strings.Split(s, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, ".")
}

// hookConflict reports whether a TOML configuration defines one of
// tend's hook events in a form that appended [[hooks.<Event>]] tables
// would conflict with: a plain array or inline table, or a [hooks.<Event>]
// table. Arrays of tables are fine, and so is anything else named hooks.
func hookConflict(config string) bool {
	events := map[string]bool{}
	for _, ev := range setupEvents {
		events[ev.name] = true
	}
	table := ""
	for _, line := range strings.Split(config, "\n") {
		if m := tomlHeader.FindStringSubmatch(line); m != nil {
			table = tomlName(m[2])
			if m[1] == "[" && strings.HasPrefix(table, "hooks.") && events[strings.TrimPrefix(table, "hooks.")] {
				return true
			}
			continue
		}
		m := tomlKey.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch key := tomlName(m[1]); table {
		case "":
			if key == "hooks" || strings.HasPrefix(key, "hooks.") && events[strings.TrimPrefix(key, "hooks.")] {
				return true
			}
		case "hooks":
			if events[key] {
				return true
			}
		}
	}
	return false
}

// tomlCommands returns the decoded values of the command keys in config.
func tomlCommands(config string) map[string]bool {
	out := map[string]bool{}
	for _, m := range tomlCommand.FindAllStringSubmatch(config, -1) {
		v := m[1]
		if v[0] == '\'' {
			out[v[1:len(v)-1]] = true
		} else if u, err := strconv.Unquote(v); err == nil {
			out[u] = true
		}
	}
	return out
}

// codexLike configures Codex or traex: an MCP server in the configuration
// that forwards TEND_ADDON and does not ask before each call, and the
// hooks. Codex reads them from hooks.json next to its configuration;
// traex 0.210 reads only the [hooks] tables of traecli.toml.
func (s *setup) codexLike() error {
	home, file := s.e.Getenv("CODEX_HOME"), "config.toml"
	def := filepath.Join(s.e.Home, ".codex")
	if s.agent == protocol.AgentTraex {
		home, file, def = s.e.Getenv("TRAE_HOME"), "traecli.toml", filepath.Join(s.e.Home, ".trae")
	}
	if home == "" {
		home = def
	}
	if s.agent == protocol.AgentCodex {
		if err := s.editJSON(filepath.Join(home, "hooks.json"), s.addHooks); err != nil {
			return err
		}
	}
	path := filepath.Join(home, file)
	before, err := readOptional(path)
	if err != nil {
		return err
	}
	block := fmt.Sprintf(`[mcp_servers.tend]
command = %q
args = ["mcp", "--agent", %q]
# The agent passes variables on to MCP servers only when listed: workers
# that inherit this configuration turn tend off through the first, and
# the second keeps the server on the hooks' configuration directory.
env_vars = [%q, %q]
default_tools_approval_mode = "approve"
`, s.tend, s.agent, AddonEnv, ConfigDirEnv)
	var add []string
	if mcpSection.MatchString(before) {
		fmt.Fprintf(s.e.Stdout, "%s already has [mcp_servers.tend]; make sure it matches:\n%s", path, block)
	} else {
		add = append(add, block)
	}
	if s.agent == protocol.AgentTraex {
		hooks := s.tomlHooks(before)
		if hooks != "" && hookConflict(before) {
			fmt.Fprintf(s.e.Stdout, "%s already defines hooks for these events; add tend's by hand:\n%s", path, hooks)
		} else if hooks != "" {
			add = append(add, hooks)
		}
	}
	if len(add) > 0 {
		after := before
		if after != "" && !strings.HasSuffix(after, "\n") {
			after += "\n"
		}
		for _, a := range add {
			if after != "" {
				after += "\n"
			}
			after += a
		}
		if err := s.change(path, before, after); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(s.e.Stdout, "%s already has tend's configuration.\n", path)
	}
	fmt.Fprintf(s.e.Stdout, "\nThe first time %s starts after this, it lists tend's two hooks as new: trust them, or they will not run.\n", s.agent)
	return nil
}

// tomlHooks returns the [[hooks.<Event>]] tables for the tend hooks that
// config does not mention yet.
func (s *setup) tomlHooks(config string) string {
	var b strings.Builder
	have := tomlCommands(config)
	for _, ev := range setupEvents {
		cmd := s.hookCommand(ev.event)
		if have[cmd] {
			continue
		}
		fmt.Fprintf(&b, "[[hooks.%s]]\n[[hooks.%s.hooks]]\ntype = \"command\"\ncommand = %q\ntimeout = %d\n", ev.name, ev.name, cmd, hookTimeout)
		// tend keeps the context within its own budget.
		b.WriteString("additionalContextLimit = 0\n")
	}
	return b.String()
}
