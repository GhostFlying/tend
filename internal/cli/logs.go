package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// maxLogText bounds what one log line shows of a message or a command.
const maxLogText = 2000

// logItem is the part of a worker item a person reads. Workers report
// traex app-server items; unknown types still get a line.
type logItem struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Command  string `json:"command"`
	ExitCode *int   `json:"exitCode"`
	Status   string `json:"status"`
	Server   string `json:"server"`
	Tool     string `json:"tool"`
	Query    string `json:"query"`
	Content  []struct {
		Text string `json:"text"`
	} `json:"content"`
	Changes []struct {
		Path string `json:"path"`
		Kind struct {
			Type string `json:"type"`
		} `json:"kind"`
	} `json:"changes"`
}

// logLine renders a run log entry for people: what the worker said, ran
// and changed. It returns false for entries not worth a line, such as
// reasoning. --json keeps the entries as they are.
func logLine(l protocol.RunLogView) (string, bool) {
	prefix := fmt.Sprintf("%s #%d ", l.CreatedAt.Local().Format("15:04:05"), l.Seq)
	if l.Kind != "item.started" && l.Kind != "item.completed" {
		body := strings.TrimSpace(string(l.Body))
		if body == "" || body == "{}" || body == "null" {
			return prefix + l.Kind, true
		}
		return prefix + l.Kind + " " + clip(body, 200), true
	}
	// Fields of other shapes than logItem expects, such as the content
	// of a reasoning item, are left empty rather than failing the line.
	var it logItem
	_ = json.Unmarshal(l.Body, &it)
	if it.Type == "" {
		return prefix + l.Kind, true
	}
	started := l.Kind == "item.started"
	var text string
	switch it.Type {
	case "agentMessage":
		if started {
			return "", false
		}
		text = clip(strings.TrimSpace(it.Text), maxLogText)
	case "userMessage":
		if started || len(it.Content) == 0 {
			return "", false
		}
		first, _, _ := strings.Cut(strings.TrimSpace(it.Content[0].Text), "\n")
		text = "brief: " + clip(first, maxLogText)
	case "commandExecution":
		// The command is clipped on its own so a long one keeps its outcome.
		text = "$ " + clip(strings.TrimSpace(it.Command), maxLogText)
		switch {
		case started:
			text += " …"
		// Only a finished command has an exit code of its own: one moved
		// to the background reports 0 and completes again when it ends.
		case it.ExitCode != nil && (it.Status == "" || it.Status == "completed" || it.Status == "failed"):
			text += fmt.Sprintf(" → exit %d", *it.ExitCode)
		case it.Status != "":
			text += " → " + it.Status
		}
	case "fileChange":
		if started {
			return "", false
		}
		var files []string
		for _, c := range it.Changes {
			files = append(files, fmt.Sprintf("%s (%s)", workspacePath(c.Path), c.Kind.Type))
		}
		// A completed item may still be a patch that failed or was declined.
		verb := "changed "
		if it.Status != "" && it.Status != "completed" {
			verb = "change " + it.Status + ": "
		}
		text = verb + clip(strings.Join(files, ", "), maxLogText)
	case "mcpToolCall", "dynamicToolCall":
		if started {
			return "", false
		}
		text = "tool " + it.Tool
		if it.Server != "" {
			text = "tool " + it.Server + "/" + it.Tool
		}
	case "webSearch":
		if started {
			return "", false
		}
		text = "search: " + it.Query
	case "reasoning":
		return "", false
	default:
		if started {
			return "", false
		}
		text = "item " + it.Type
	}
	return prefix + strings.ReplaceAll(text, "\n", "\n    "), true
}

// workspacePath shortens a path inside a runner's workspace or scratch
// directory to the part a person recognizes.
func workspacePath(p string) string {
	for _, dir := range []string{"/workspaces/", "/scratch/"} {
		if i := strings.LastIndex(p, dir); i >= 0 {
			if _, rest, ok := strings.Cut(p[i+len(dir):], "/"); ok {
				if dir == "/scratch/" {
					return "scratch:" + rest
				}
				return rest
			}
		}
	}
	return p
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
