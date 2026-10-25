// Package orca drives orca through its CLI (D63, D65, D66). The runner
// lays out single-repository workspaces as orca worktrees and runs agent
// TUIs in them; this package is the typed surface of the orca commands it
// needs, and the TUI worker built on it.
package orca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
)

// Exec runs the orca CLI with args in dir and returns its standard
// output. A command that fails with a JSON reply on standard output is not
// an error of Exec: the reply says what went wrong.
type Exec func(ctx context.Context, dir string, args []string) ([]byte, error)

// Client runs orca commands. The zero value runs `orca` from PATH.
type Client struct {
	// Bin is the orca CLI. Default "orca".
	Bin string
	// Exec replaces running the CLI, for tests.
	Exec Exec
}

// Error is a failure orca reported.
type Error struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("orca: %s: %s", e.Code, e.Message) }

// field returns a string field of the error's data, or "".
func (e *Error) field(name string) string {
	var data map[string]any
	if json.Unmarshal(e.Data, &data) != nil {
		return ""
	}
	s, _ := data[name].(string)
	return s
}

// LaunchRetryID returns the operation orca accepted before the reply of a
// launch was lost, for replaying it with LaunchOptions.RetryLaunch, or "".
func LaunchRetryID(err error) string {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.field("operationId")
	}
	return ""
}

// SendRetryID returns the request orca accepted before the reply of a send
// was lost, for replaying it with SendOptions.RetryRequest, or "". orca
// reports it for every mutation whose reply it could not confirm.
func SendRetryID(err error) string {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.field("orchestrationRequestId")
	}
	return ""
}

// Unconfirmed reports whether err leaves open whether orca carried out the
// request: anything but a reply in which orca refused it. The CLI reports
// a lost or late answer from orca's runtime as a runtime_* error, and an
// unconfirmed mutation with the ID to replay it with.
func Unconfirmed(err error) bool {
	e, ok := errors.AsType[*Error](err)
	if !ok {
		return true
	}
	return strings.HasPrefix(e.Code, "runtime_") || e.Code == "agent_session_operation_unknown" ||
		e.field("operationId") != "" || e.field("orchestrationRequestId") != ""
}

type reply struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// call runs one command with --json in dir and decodes its result into
// out, which may be nil.
func (c *Client) call(ctx context.Context, dir string, out any, args ...string) error {
	args = append(args, "--json")
	run := c.Exec
	if run == nil {
		run = c.runCLI
	}
	stdout, runErr := run(ctx, dir, args)
	var r reply
	if err := json.Unmarshal(stdout, &r); err != nil {
		if runErr != nil {
			return fmt.Errorf("orca %s: %w", strings.Join(args[:2], " "), runErr)
		}
		return fmt.Errorf("orca %s: decode reply: %w", strings.Join(args[:2], " "), err)
	}
	if !r.OK {
		if r.Error == nil {
			return fmt.Errorf("orca %s: failed without an error", strings.Join(args[:2], " "))
		}
		return r.Error
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(r.Result, out); err != nil {
		return fmt.Errorf("orca %s: decode result: %w", strings.Join(args[:2], " "), err)
	}
	return nil
}

func (c *Client) runCLI(ctx context.Context, dir string, args []string) ([]byte, error) {
	bin := c.Bin
	if bin == "" {
		bin = "orca"
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	procx.Contain(cmd)
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[:2000] + "…"
		}
		err = fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.Bytes(), err
}

// Repo is a repository registered in orca.
type Repo struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	DisplayName string `json:"displayName"`
}

// Repo returns the repository a selector such as name:<name> names.
func (c *Client) Repo(ctx context.Context, selector string) (Repo, error) {
	var res struct {
		Repo Repo `json:"repo"`
	}
	err := c.call(ctx, "", &res, "repo", "show", "--repo", selector)
	return res.Repo, err
}

// Worktree is an orca-managed checkout.
type Worktree struct {
	// ID selects the worktree as id:<ID>.
	ID          string `json:"id"`
	RepoID      string `json:"repoId"`
	Path        string `json:"path"`
	Head        string `json:"head"`
	Branch      string `json:"branch"`
	DisplayName string `json:"displayName"`
	BaseRef     string `json:"baseRef,omitempty"`
	Comment     string `json:"comment,omitempty"`
	InstanceID  string `json:"instanceId,omitempty"`
	// IsMainWorktree marks the repository's own checkout.
	IsMainWorktree bool `json:"isMainWorktree"`
}

// BranchName returns the worktree's branch without the refs/heads/ prefix.
func (w Worktree) BranchName() string { return strings.TrimPrefix(w.Branch, "refs/heads/") }

// Selector returns the selector of the worktree.
func (w Worktree) Selector() string { return "id:" + w.ID }

// Worktrees lists the worktrees of repo, a repository selector.
func (c *Client) Worktrees(ctx context.Context, repo string) ([]Worktree, error) {
	var res struct {
		Worktrees []Worktree `json:"worktrees"`
		Truncated bool       `json:"truncated"`
	}
	if err := c.call(ctx, "", &res, "worktree", "list", "--repo", repo, "--limit", "10000"); err != nil {
		return nil, err
	}
	if res.Truncated {
		return nil, fmt.Errorf("orca worktree list: more than 10000 worktrees in %s", repo)
	}
	return res.Worktrees, nil
}

// Worktree returns the worktree a selector such as path:<dir> names.
func (c *Client) Worktree(ctx context.Context, selector string) (Worktree, error) {
	var res struct {
		Worktree Worktree `json:"worktree"`
	}
	err := c.call(ctx, "", &res, "worktree", "show", "--worktree", selector)
	return res.Worktree, err
}

// CreateWorktree describes a new worktree.
type CreateWorktree struct {
	// Repo is the repository selector, such as id:<repoId>.
	Repo    string
	Name    string
	Comment string
	// BaseBranch is the ref the worktree starts from; empty means the
	// repository's default.
	BaseBranch string
}

// CreateWorktree creates a worktree with no parent and without running
// the repository's setup, which tend's profiles take care of.
func (c *Client) CreateWorktree(ctx context.Context, o CreateWorktree) (Worktree, error) {
	args := []string{"worktree", "create", "--repo", o.Repo, "--name", o.Name, "--no-parent", "--setup", "skip"}
	if o.Comment != "" {
		args = append(args, "--comment", o.Comment)
	}
	if o.BaseBranch != "" {
		args = append(args, "--base-branch", o.BaseBranch)
	}
	var res struct {
		Worktree Worktree `json:"worktree"`
	}
	if err := c.call(ctx, "", &res, args...); err != nil {
		return Worktree{}, err
	}
	return res.Worktree, nil
}

// RemoveWorktree removes a worktree, discarding what is in it, and lets
// orca delete its branch when it can prove the branch is merged.
func (c *Client) RemoveWorktree(ctx context.Context, worktree string) error {
	return c.call(ctx, "", nil, "worktree", "rm", "--worktree", worktree, "--force")
}

// LaunchOptions start an agent in a worktree.
type LaunchOptions struct {
	// Dir is where the CLI runs. A replay must run from the same
	// directory as the launch it replays.
	Dir      string
	Agent    string
	Worktree string
	Prompt   string
	Model    string
	Effort   string
	// RetryLaunch replays the launch orca accepted under this operation.
	RetryLaunch string
}

// Launch is what an agent launch started.
type Launch struct {
	Outcome struct {
		// Kind is "terminal" or "structured" (a chat).
		Kind    string `json:"kind"`
		Handle  string `json:"handle"`
		PaneKey string `json:"paneKey"`
	} `json:"outcome"`
	WorktreeID string `json:"worktreeId"`
	Receipt    struct {
		Detail string `json:"detail"`
	} `json:"receipt"`
	Prompt struct {
		// Outcome says whether the brief reached the agent.
		Outcome string `json:"outcome"`
	} `json:"prompt"`
	OperationID string `json:"operationId"`
}

// OutcomeTerminal is the outcome kind of an agent launched in a terminal;
// the other kind, structured, is a chat.
const OutcomeTerminal = "terminal"

// LaunchAgent starts an agent, or replays the launch named by
// RetryLaunch.
func (c *Client) LaunchAgent(ctx context.Context, o LaunchOptions) (Launch, error) {
	args := []string{"agent", "launch", "--agent", o.Agent, "--worktree", o.Worktree}
	if o.Prompt != "" {
		args = append(args, "--prompt", o.Prompt)
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
		if o.Effort != "" {
			args = append(args, "--effort", o.Effort)
		}
	}
	if o.RetryLaunch != "" {
		args = append(args, "--retry-launch", o.RetryLaunch)
	}
	var res Launch
	err := c.call(ctx, o.Dir, &res, args...)
	return res, err
}

// SendOptions is input for a terminal.
type SendOptions struct {
	Terminal string
	Text     string
	// Enter submits Text as a prompt.
	Enter bool
	// WaitSubmit observes the prompt this long for the agent's turn to
	// start; it never resends.
	WaitSubmit time.Duration
	// RetryRequest replays the send orca accepted under this request.
	RetryRequest string
}

// Send is the receipt of a send.
type Send struct {
	Handle   string `json:"handle"`
	Accepted bool   `json:"accepted"`
	// Prompt is set for a text-plus-Enter prompt to an agent.
	Prompt *struct {
		RequestID string `json:"requestId"`
		// Stages grow from input_accepted to turn_started.
		Stages []string `json:"stages"`
		// BaselineWorkingSequence is the agent's working sequence when the
		// prompt was accepted.
		BaselineWorkingSequence int64 `json:"baselineWorkingSequence"`
	} `json:"prompt"`
}

// Send stages.
const (
	StageInputAccepted = "input_accepted"
	StageTurnStarted   = "turn_started"
)

// SendInput writes to a terminal.
func (c *Client) SendInput(ctx context.Context, o SendOptions) (Send, error) {
	args := []string{"terminal", "send", "--terminal", o.Terminal, "--text", o.Text}
	if o.Enter {
		args = append(args, "--enter")
	}
	if o.WaitSubmit > 0 {
		args = append(args, "--wait-submit", strconv.Itoa(int(o.WaitSubmit.Seconds())))
	}
	if o.RetryRequest != "" {
		args = append(args, "--retry-request", o.RetryRequest)
	}
	var res struct {
		Send Send `json:"send"`
	}
	err := c.call(ctx, "", &res, args...)
	return res.Send, err
}

// Wait conditions.
const (
	WaitExit    = "exit"
	WaitTUIIdle = "tui-idle"
)

// Wait is the state of a terminal when a wait ended.
type Wait struct {
	Satisfied bool   `json:"satisfied"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exitCode"`
	// BlockedReason says what the agent waits for, such as
	// agent-trust-workspace.
	BlockedReason string `json:"blockedReason,omitempty"`
}

// WaitFor waits up to timeout for a terminal condition.
func (c *Client) WaitFor(ctx context.Context, terminal, condition string, timeout time.Duration) (Wait, error) {
	var res struct {
		Wait Wait `json:"wait"`
	}
	err := c.call(ctx, "", &res, "terminal", "wait", "--terminal", terminal, "--for", condition,
		"--timeout-ms", strconv.FormatInt(timeout.Milliseconds(), 10))
	return res.Wait, err
}

// CloseTerminal stops one terminal and removes it.
func (c *Client) CloseTerminal(ctx context.Context, terminal string) error {
	return c.call(ctx, "", nil, "terminal", "close", "--terminal", terminal)
}

// Terminal is a live orca-managed terminal.
type Terminal struct {
	Handle     string `json:"handle"`
	WorktreeID string `json:"worktreeId"`
	Connected  bool   `json:"connected"`
	TabID      string `json:"tabId"`
	LeafID     string `json:"leafId"`
}

// PaneKey is the key orca's agents name their terminal by.
func (t Terminal) PaneKey() string { return t.TabID + ":" + t.LeafID }

// Terminals lists the live terminals of a worktree.
func (c *Client) Terminals(ctx context.Context, worktree string) ([]Terminal, error) {
	var res struct {
		Terminals []Terminal `json:"terminals"`
	}
	err := c.call(ctx, "", &res, "terminal", "list", "--worktree", worktree, "--limit", "1000")
	return res.Terminals, err
}

// MainAgent is the root turn's state, independently of background child work.
// Older Orca hosts omit it; absence is not evidence of success or failure.
type MainAgent struct {
	State   string `json:"state"`
	Outcome string `json:"outcome,omitempty"`

	StateStartedAt int64 `json:"stateStartedAt"`
}

// ProviderSession identifies the root transcript used for a recovery handoff.
type ProviderSession struct {
	ID             string `json:"id"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
}

// ChildWork is Orca's explicit background-operation settlement evidence.
type ChildWork struct {
	State      string `json:"state"`
	Membership string `json:"membership"`
}

// Agent is an agent orca tracks in a worktree.
type Agent struct {
	PaneKey             string           `json:"paneKey"`
	MainAgent           *MainAgent       `json:"mainAgent,omitempty"`
	ProviderSession     *ProviderSession `json:"providerSession,omitempty"`
	Children            []ChildWork      `json:"children,omitempty"`
	SubagentObservation string           `json:"subagentObservation,omitempty"`
	// State is working, done and so on, as orca reports it.
	State     string `json:"state"`
	AgentType string `json:"agentType"`
	// Prompt is the agent's latest prompt.
	Prompt               string `json:"prompt"`
	ToolName             string `json:"toolName"`
	ToolInput            string `json:"toolInput"`
	LastAssistantMessage string `json:"lastAssistantMessage"`
	Interrupted          bool   `json:"interrupted"`
	StateStartedAt       int64  `json:"stateStartedAt"`
	UpdatedAt            int64  `json:"updatedAt"`
}

// Agents returns the agents orca tracks in the worktree with the given
// ID.
func (c *Client) Agents(ctx context.Context, worktreeID string) ([]Agent, error) {
	var res struct {
		Worktrees []struct {
			WorktreeID string  `json:"worktreeId"`
			Agents     []Agent `json:"agents"`
		} `json:"worktrees"`
		Truncated bool `json:"truncated"`
	}
	if err := c.call(ctx, "", &res, "worktree", "ps", "--limit", "10000"); err != nil {
		return nil, err
	}
	for _, w := range res.Worktrees {
		if w.WorktreeID == worktreeID {
			return w.Agents, nil
		}
	}
	if res.Truncated {
		return nil, fmt.Errorf("orca worktree ps: more than 10000 worktrees")
	}
	return nil, nil
}
