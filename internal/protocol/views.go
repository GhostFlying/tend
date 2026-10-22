package protocol

import (
	"encoding/json"
	"time"
)

// Read views: the shapes in which the main agent's tools and the owner API
// show the ledger. Both use the same views so they cannot drift apart.

// TaskSummary is a task as listed.
type TaskSummary struct {
	TaskID          string        `json:"task_id"`
	Title           string        `json:"title"`
	Project         string        `json:"project"`
	Lifecycle       TaskLifecycle `json:"lifecycle"`
	ControlMode     ControlMode   `json:"control_mode"`
	ControlRevision int64         `json:"control_revision"`
	SpecVersion     int64         `json:"spec_version"`
	Rating          Rating        `json:"rating"`
	BlockReason     BlockReason   `json:"block_reason"`
	Workspace       string        `json:"workspace"`
	// NextImplementGeneration is the generation a retry of the
	// implementation must name.
	NextImplementGeneration int64 `json:"next_implement_generation"`
}

// TaskDetail is one task in full.
type TaskDetail struct {
	TaskSummary
	Spec        json.RawMessage  `json:"spec"`
	Actions     []ActionView     `json:"actions"`
	Runs        []RunView        `json:"runs"`
	Artifacts   []ArtifactView   `json:"artifacts"`
	Acceptances []AcceptanceView `json:"acceptances"`
	Decisions   []DecisionView   `json:"decisions"`
	// WorkspaceState is where the workspace was placed and how its mounts
	// resolved, once a runner has laid it out.
	WorkspaceState *WorkspaceView `json:"workspace_state,omitempty"`
}

// WorkspaceView is a task's workspace on a host.
type WorkspaceView struct {
	Host   string      `json:"host"`
	Status string      `json:"status"`
	Mounts []MountView `json:"mounts"`
}

// MountView is how one mount of a workspace was laid out.
type MountView struct {
	Path            string          `json:"path"`
	Repo            string          `json:"repo"`
	Base            json.RawMessage `json:"base"`
	ResolvedBaseSHA string          `json:"resolved_base_sha"`
	ResolvedFrom    string          `json:"resolved_from"`
	Branch          string          `json:"branch"`
}

// ActionView is a plan action of a task.
type ActionView struct {
	ActionID    string       `json:"action_id"`
	Kind        ActionKind   `json:"kind"`
	Status      ActionStatus `json:"status"`
	Generation  int64        `json:"generation"`
	SpecVersion int64        `json:"spec_version"`
	ArtifactID  string       `json:"artifact_id,omitempty"`
}

// RunView is a run of a task.
type RunView struct {
	RunID      string      `json:"run_id"`
	Role       RunRole     `json:"role"`
	Generation int64       `json:"generation"`
	Host       string      `json:"host"`
	Observed   RunObserved `json:"observed"`
}

// ArtifactView is an artifact a run submitted.
type ArtifactView struct {
	ArtifactID string          `json:"artifact_id"`
	RunID      string          `json:"run_id"`
	Ref        json.RawMessage `json:"ref"`
}

// AcceptanceView is a verdict on an artifact.
type AcceptanceView struct {
	ArtifactID  string          `json:"artifact_id"`
	SpecVersion int64           `json:"spec_version"`
	Verdict     Verdict         `json:"verdict"`
	Issues      json.RawMessage `json:"issues"`
	Evidence    json.RawMessage `json:"evidence,omitempty"`
}

// DecisionView is a question for the owner.
type DecisionView struct {
	DecisionID string `json:"decision_id"`
	TaskID     string `json:"task_id,omitempty"`
	// Project is the project of the task, in the snapshot.
	Project  string          `json:"project,omitempty"`
	Question string          `json:"question"`
	Options  json.RawMessage `json:"options"`
	// Effects maps the options tend carries out itself to what it does
	// (D53).
	Effects map[string]DecisionEffect `json:"effects,omitempty"`
	Version int64                     `json:"version"`
	Status  string                    `json:"status"`
	Answer  json.RawMessage           `json:"answer,omitempty"`
}

// ArtifactDetail is an artifact with every verdict on it.
type ArtifactDetail struct {
	Artifact    ArtifactView     `json:"artifact"`
	TaskID      string           `json:"task_id"`
	Acceptances []AcceptanceView `json:"acceptances"`
}

// RequestView is the recorded outcome of a request.
type RequestView struct {
	RequestID string          `json:"request_id"`
	Operation string          `json:"operation"`
	Status    OpStatus        `json:"status"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}

// HostView is an active execution host.
type HostView struct {
	HostID string   `json:"host_id"`
	OS     string   `json:"os"`
	Arch   string   `json:"arch"`
	Labels []string `json:"labels"`
	Slots  int64    `json:"slots"`
	// Worker is the harness the host runs its workers with (D64).
	Worker WorkerKind `json:"worker"`
	// OpenRuns counts the runs on the host that may still be working.
	OpenRuns int64 `json:"open_runs"`
}

// ProjectView is a project with its current load.
type ProjectView struct {
	Project string `json:"project"`
	Title   string `json:"title"`
	// Weight multiplies the rating of the project's tasks in the queue.
	Weight  int64 `json:"weight"`
	Retired bool  `json:"retired,omitempty"`
	// OpenTasks counts the project's unfinished tasks, of which a
	// snapshot may list only some.
	OpenTasks int64 `json:"open_tasks"`
	// OpenRuns counts the project's runs that may still be working.
	OpenRuns int64 `json:"open_runs"`
	// Queued counts the project's entries in the queue.
	Queued int64 `json:"queued"`
}

// QueueEntry is a queued action that waits only for a host to take it.
type QueueEntry struct {
	// Place is the entry's place in the whole queue, from 1.
	Place    int        `json:"place"`
	ActionID string     `json:"action_id"`
	TaskID   string     `json:"task_id"`
	Title    string     `json:"title"`
	Project  string     `json:"project"`
	Kind     ActionKind `json:"kind"`
	Rating   Rating     `json:"rating"`
	// WaitedMillis is how long the action has been queued.
	WaitedMillis int64 `json:"waited_millis"`
	// Score is the action's score, what orders everything but p0 (D60).
	Score float64 `json:"score"`
	// Unplaceable says why no active host can take the action, such as
	// a workspace no host's worker supports (D64); it is empty when some
	// host can once it has a free slot.
	Unplaceable string `json:"unplaceable,omitempty"`
}

// Snapshot is the owner's overview of the ledger, read at one point.
type Snapshot struct {
	// EventSeq is the last event the snapshot reflects.
	EventSeq int64 `json:"event_seq"`
	// Tasks are the unfinished tasks.
	Tasks []TaskSummary `json:"tasks"`
	// Decisions are the open decisions.
	Decisions []DecisionView `json:"decisions"`
	Hosts     []HostView     `json:"hosts"`
	// Projects are the active projects and the retired ones that still
	// have unfinished tasks.
	Projects []ProjectView `json:"projects"`
	// Queue lists the queued actions in the order hosts take them; one a
	// host cannot run is passed over for the next it can.
	Queue []QueueEntry `json:"queue"`
}

// TaskPage is one page of a task listing.
type TaskPage struct {
	Tasks []TaskSummary `json:"tasks"`
	// Next is an opaque cursor for the following page, passed back as
	// before; it is empty on the last page.
	Next string `json:"next,omitempty"`
}

// RunLogView is a run fact that does not change state.
type RunLogView struct {
	Seq       int64           `json:"seq"`
	NativeID  string          `json:"native_id,omitempty"`
	Kind      string          `json:"kind"`
	Body      json.RawMessage `json:"body"`
	CreatedAt time.Time       `json:"created_at"`
}

// RunLogPage is one page of a run's logs.
type RunLogPage struct {
	RunID string `json:"run_id"`
	// Observed is the run's state when the page was read. Once the run
	// has ended, waiting for more logs is pointless.
	Observed RunObserved  `json:"observed"`
	Logs     []RunLogView `json:"logs"`
	// Next is the after cursor for the following page: the last seq
	// returned, or the given one if the page is empty.
	Next int64 `json:"next"`
}

// Conversation item kinds (D69).
const (
	ItemMessage = "message"
	ItemToolUse = "tool_use"
)

// ConversationItem is one item of the conversation: a message of the
// owner or the main agent, or a tool the main agent used. Items share
// one order, by seq.
type ConversationItem struct {
	Seq int64 `json:"seq"`
	// Kind is "message" or "tool_use".
	Kind      string `json:"kind"`
	MessageID string `json:"message_id"`
	// Author is "owner" or "main_agent".
	Author string `json:"author"`
	// Body is a message's text; empty for a tool use.
	Body string `json:"body,omitempty"`
	// ReplyTo is the message an owner message replies to, or the owner
	// message whose turn produced an item of the main agent.
	ReplyTo   string    `json:"reply_to,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// State is how far the main agent got with an owner message.
	State MessageState `json:"state,omitempty"`
	// AssistantState is set on a streamed main-agent message (D74).
	AssistantState AssistantState `json:"assistant_state,omitempty"`
	// Decision is the decision option an owner message chooses (D71).
	Decision *DecisionChoice `json:"decision,omitempty"`
	// Tool is set for a tool use.
	Tool *ToolUseView `json:"tool,omitempty"`
}

// ToolUseView is a tool the main agent used. Input and output are cut off
// with a note past a size limit.
type ToolUseView struct {
	Name      string     `json:"name"`
	Input     string     `json:"input,omitempty"`
	Output    string     `json:"output,omitempty"`
	Status    ToolStatus `json:"status"`
	TurnID    string     `json:"turn_id"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ConversationPage is a run of conversation items in order.
type ConversationPage struct {
	Items []ConversationItem `json:"items"`
	// More tells whether further items exist in the direction read:
	// earlier ones for a page read before a seq or the latest page, later
	// ones for a page read after a seq.
	More bool `json:"more"`
}

// LedgerEvent tells an owner's client that something in the ledger
// changed (D69). It names what changed and leaves reading it to the
// client, except for the conversation, whose events carry the item as it
// is now.
type LedgerEvent struct {
	// EventSeq orders the events; a client resumes the stream after the
	// last one it received.
	EventSeq   int64     `json:"event_seq"`
	ObjectType string    `json:"object_type"`
	ObjectID   string    `json:"object_id"`
	Kind       string    `json:"kind"`
	CreatedAt  time.Time `json:"created_at"`
	// Item is the conversation item a conversation event is about.
	Item *ConversationItem `json:"item,omitempty"`
}

// StreamStart is the first message of the event stream: the cursor the
// stream continues from. A client that connected without a cursor reads
// the views it shows after receiving it, so it misses nothing.
type StreamStart struct {
	EventSeq int64 `json:"event_seq"`
}
