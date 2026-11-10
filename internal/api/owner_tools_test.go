package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

func mainOwnerFixture(t *testing.T) (*env, protocol.OwnerToolCall) {
	t.Helper()
	e := newEnv(t)
	e.configure()
	zero := int64(0)
	put := e.envelope("categories.put", protocol.Target{Type: "category", ID: "planner"}, &zero, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"})
	if op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}, &put, app.PutAgentCategory{}); err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "manager", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "HOSTED_EXECUTABLE", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "manager", Revision: 1, DisplayName: "Manager", RuntimeID: "runtime", Categories: []string{"planner"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: "model", Enabled: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	e.exec("hosted.configure", protocol.Target{Type: "hosted_configuration"}, new(int64), map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, app.AcceptHostedConfiguration{Prepared: prepared})
	e.app = app.New(e.store, e.ids, e.clock.Now, app.WithHostedConfiguration(prepared), app.WithMainOwner(ownerLogin))
	e.server.app = e.app
	e.server.cfg.HostedMainAgent = true
	selected, err := prepared.Select("manager", agentconfig.Main)
	if err != nil {
		t.Fatal(err)
	}
	request, message := e.ids.New(protocol.PrefixRequest), e.ids.New(protocol.PrefixMessage)
	err = e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: message, Author: "owner", Body: "Manage the requested work", CreatedAt: e.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		if err := tx.PutMainTurn(t.Context(), store.MainTurn{RequestID: request, MessageID: message, ThreadID: "main-native", TurnID: "turn", Status: store.TurnStarted}, e.clock.Now().UnixMilli()); err != nil {
			return err
		}
		return tx.InsertHostedExecution(t.Context(), store.HostedExecution{ID: request, ConversationID: "main", Selection: selected, CreatedAt: e.clock.Now().UnixMilli()})
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, protocol.OwnerToolCall{Actor: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main-harness", Thread: "main-native"}, ExecutionID: request}
}

func ownerToolInvoke(t *testing.T, e *env, base protocol.OwnerToolCall, operation string, args protocol.OwnerToolArguments) (protocol.APIResponse, error) {
	t.Helper()
	base.Name = ownerToolName(operation)
	if base.RequestID == "" {
		base.RequestID = e.ids.New(protocol.PrefixRequest)
	}
	base.Arguments, _ = json.Marshal(args)
	value, err := e.server.MainOwnerTools().Call(t.Context(), base)
	data, _ := json.Marshal(value)
	var response protocol.APIResponse
	if len(data) > 0 {
		if e := json.Unmarshal(data, &response); e != nil {
			t.Fatal(e, string(data))
		}
	}
	return response, err
}

func TestMainOwnerUsesRealOwnerCommandsAndRetainsActor(t *testing.T) {
	e, call := mainOwnerFixture(t)
	create, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Original responsibility", Project: "default", Intent: "Investigate", Stages: []protocol.IssueStage{}}})
	args := protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "issue"}, Payload: create}
	call.RequestID = e.ids.New(protocol.PrefixRequest)
	response, err := ownerToolInvoke(t, e, call, "issues.create", args)
	if err != nil || response.Operation == nil || response.Operation.Error != nil {
		t.Fatal(response, err)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(response.Operation.Result, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Assignee.Kind != protocol.IssueAssigneeOwner || issue.Assignee.Ref != ownerLogin {
		t.Fatal(issue)
	}
	replay, err := ownerToolInvoke(t, e, call, "issues.create", args)
	if err != nil || string(replay.Operation.Result) != string(response.Operation.Result) {
		t.Fatal(replay, err)
	}
	if n := e.count("SELECT COUNT(*) FROM work_items"); n != 1 {
		t.Fatal("duplicate create", n)
	}
	err = e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var actor string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT actor_json FROM issue_control_revisions WHERE issue_id=? AND revision=1`, issue.IssueID).Scan(&actor); err != nil {
			return err
		}
		if !strings.Contains(actor, `"kind":"main_agent"`) {
			t.Fatal("actor replaced by human", actor)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	status, _ := json.Marshal(protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueInProgress})
	response, err = ownerToolInvoke(t, e, protocol.OwnerToolCall{Actor: call.Actor, ExecutionID: call.ExecutionID}, "issues.set_status", protocol.OwnerToolArguments{EffectSeq: 2, Target: protocol.Target{Type: "issue", ID: issue.IssueID}, ExpectedControlRevision: &issue.Revision, Payload: status})
	if err != nil {
		t.Fatal(response, err)
	}
	if err := json.Unmarshal(response.Operation.Result, &issue); err != nil {
		t.Fatal(err)
	}
	cancel, _ := json.Marshal(protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Owner requests cancellation"})
	response, err = ownerToolInvoke(t, e, protocol.OwnerToolCall{Actor: call.Actor, ExecutionID: call.ExecutionID}, "issues.cancel", protocol.OwnerToolArguments{EffectSeq: 3, Target: protocol.Target{Type: "issue", ID: issue.IssueID}, ExpectedControlRevision: &issue.Revision, Payload: cancel})
	if err != nil {
		t.Fatal(response, err)
	}
	if err := json.Unmarshal(response.Operation.Result, &issue); err != nil || issue.State != protocol.IssueCancelling {
		t.Fatal(issue, err)
	}
}

func TestMainOwnerCatalogMatchesOwnerAndRejectsOtherActors(t *testing.T) {
	e, call := mainOwnerFixture(t)
	g := e.server.MainOwnerTools()
	seen := map[string]bool{}
	for _, spec := range g.Specs() {
		if seen[spec.Name] || len(spec.Name) > 64 {
			t.Fatal("invalid tool identity", spec.Name)
		}
		seen[spec.Name] = true
		var schema map[string]any
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema["properties"].(map[string]any)["payload"] == nil {
			t.Fatal("invalid argument schema", spec.Name)
		}
	}
	if len(seen) != len(OwnerCapabilities())-1 {
		t.Fatal(len(seen), len(OwnerCapabilities()))
	}
	call.Name = ownerToolName("GET /api/v1/issues")
	call.Arguments = json.RawMessage(`{}`)
	value, err := g.Call(t.Context(), call)
	if err != nil || value == nil {
		t.Fatal(value, err)
	}
	for _, actor := range []protocol.Principal{{Kind: protocol.PrincipalBackgroundAgent, Subject: "issue-agent", Thread: "main-native"}, {Kind: protocol.PrincipalMainAgent, Subject: "main-harness", Thread: "other-thread"}, {Kind: protocol.PrincipalRun, Subject: "run", Role: protocol.RoleIssueManager}} {
		call.Actor = actor
		if _, err := g.Call(t.Context(), call); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal(actor, err)
		}
	}
}

func TestMainOwnerReplaysOriginalOwnerDomainAndReadsMainReceipts(t *testing.T) {
	e, call := mainOwnerFixture(t)
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}
	create := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Keep original request", Project: "default", Intent: "Report", Stages: []protocol.IssueStage{}}}
	original := e.envelope("issues.create", protocol.Target{Type: "issue"}, nil, create)
	created, err := e.app.Execute(t.Context(), owner, &original, app.CanonicalOwnerIssueCommand{Action: "create"})
	if err != nil || created.Error != nil {
		t.Fatal(created, err)
	}
	response, err := ownerToolInvoke(t, e, call, "issues.create", protocol.OwnerToolArguments{OriginalEnvelope: &original})
	var replayed, recorded protocol.Issue
	if err != nil || response.Operation == nil {
		t.Fatal(response, err)
	}
	if err := json.Unmarshal(response.Operation.Result, &replayed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(created.Result, &recorded); err != nil {
		t.Fatal(err)
	}
	if replayed.IssueID != recorded.IssueID || replayed.Revision != recorded.Revision || response.Operation.RequestID != original.RequestID {
		t.Fatal(response, created)
	}
	if e.count(`SELECT COUNT(*) FROM work_items`) != 1 {
		t.Fatal("owner request replayed as a new main request")
	}
	var issue protocol.Issue
	if err := json.Unmarshal(created.Result, &issue); err != nil {
		t.Fatal(err)
	}
	bad, _ := json.Marshal(protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueInProgress})
	call.RequestID = e.ids.New(protocol.PrefixRequest)
	stale := issue.Revision + 10
	failed, err := ownerToolInvoke(t, e, call, "issues.set_status", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "issue", ID: issue.IssueID}, ExpectedControlRevision: &stale, Payload: bad})
	if protocol.CodeOf(err) != protocol.CodeStaleRevision || failed.CurrentIssue == nil {
		t.Fatal(failed, err)
	}
	reader := call
	reader.RequestID = ""
	reader.Name = ownerToolName("GET /api/v1/requests/{id}")
	reader.Arguments, _ = json.Marshal(protocol.OwnerToolArguments{Path: map[string]string{"id": call.RequestID}})
	value, err := e.server.MainOwnerTools().Call(t.Context(), reader)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(value)
	var receipt protocol.RequestView
	if err := json.Unmarshal(data, &receipt); err != nil || receipt.Status != protocol.OpFailed || receipt.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(string(data), err)
	}
	var other protocol.RequestView
	if status := e.do("GET", "/api/v1/requests/"+call.RequestID, e.login(protocol.ScopeOwner), nil, &other); status != 200 || other.Status != receipt.Status {
		t.Fatal(status, other)
	}
}

func TestMainOwnerToolCallbackUsesOwnerAuthority(t *testing.T) {
	e, call := mainOwnerFixture(t)
	e.tools = tools.New(e.app, e.store, e.context, quiet())
	e.tools.SetOwnerTools(e.server.MainOwnerTools())
	for _, purpose := range []agentconfig.Purpose{agentconfig.IssueManager, agentconfig.Consultation} {
		for _, spec := range e.tools.NamedSpecs(purpose) {
			if strings.HasPrefix(spec.Name, "owner_") {
				t.Fatal("owner tool exposed to scoped role", purpose, spec.Name)
			}
		}
	}
	payload := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Actual tool callback", Project: "default", Intent: "Report", Stages: []protocol.IssueStage{}}}
	raw, _ := json.Marshal(map[string]any{"effect_seq": 1, "payload": payload})
	result := e.tools.Handle(t.Context(), tools.Call{Principal: call.Actor, ExecutionRequestID: call.ExecutionID, TurnID: "turn", Native: "call-1", Tool: "issues_create", Arguments: raw})
	if !result.Success {
		t.Fatal(result.Text)
	}
	if e.count(`SELECT COUNT(*) FROM work_items`) != 1 {
		t.Fatal(result.Text)
	}
	result = e.tools.Handle(t.Context(), tools.Call{Principal: call.Actor, ExecutionRequestID: call.ExecutionID, TurnID: "turn", Native: "call-1", Tool: "issues_create", Arguments: raw})
	if !result.Success || e.count(`SELECT COUNT(*) FROM work_items`) != 1 {
		t.Fatal(result.Text)
	}
	task := e.task("Control original worker")
	mode, _ := json.Marshal(protocol.SetControlModePayload{Mode: protocol.ControlHumanControl})
	response, err := ownerToolInvoke(t, e, call, "control.set_mode", protocol.OwnerToolArguments{EffectSeq: 2, Target: protocol.Target{Type: "task", ID: task}, ExpectedControlRevision: new(int64(1)), Payload: mode})
	if err != nil {
		t.Fatal(response, err)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetTask(t.Context(), tx.Q(), task)
		if err == nil && row.ControlMode != protocol.ControlHumanControl {
			t.Fatal(row)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMainOwnerNativeEffectReplayAndTurnFence(t *testing.T) {
	e, call := mainOwnerFixture(t)
	payload, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Exact native receipt", Project: "default", Intent: "Record", Stages: []protocol.IssueStage{}}})
	args := protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "issue"}, Payload: payload}
	call.RequestID = e.ids.New(protocol.PrefixRequest)
	first, err := ownerToolInvoke(t, e, call, "issues.create", args)
	if err != nil {
		t.Fatal(first, err)
	}
	duplicate := call
	duplicate.RequestID = e.ids.New(protocol.PrefixRequest)
	if _, err := ownerToolInvoke(t, e, duplicate, "issues.create", args); protocol.CodeOf(err) != protocol.CodeDuplicateEffect {
		t.Fatal(err)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE main_turns SET status='completed' WHERE request_id=?`, call.ExecutionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if response, err := ownerToolInvoke(t, e, call, "issues.create", args); err != nil || response.Operation.RequestID != first.Operation.RequestID {
		t.Fatal(response, err)
	}
	args.EffectSeq = 2
	if _, err := ownerToolInvoke(t, e, duplicate, "issues.create", args); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM work_items`) != 1 {
		t.Fatal("duplicate native effect")
	}
}

func TestMainOwnerDiscoveryAndInvocationUseActualCatalog(t *testing.T) {
	e, call := mainOwnerFixture(t)
	service := tools.New(e.app, e.store, e.context, quiet())
	service.SetOwnerTools(e.server.MainOwnerTools())
	specs := service.NamedSpecs(agentconfig.Main)
	if len(specs) > len(tools.NamedSpecs(agentconfig.Main))+2 {
		t.Fatal("complete catalog expanded into every turn", len(specs))
	}
	invoke := func(name string, args any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		result := service.Handle(t.Context(), tools.Call{Principal: call.Actor, ExecutionRequestID: call.ExecutionID, TurnID: "turn", Native: e.ids.New(protocol.PrefixRequest), Tool: name, Arguments: raw})
		if !result.Success {
			t.Fatal(result.Text)
		}
		var response struct {
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal([]byte(result.Text), &response); err != nil {
			t.Fatal(err)
		}
		return response.Result
	}
	first := invoke("owner_capabilities", map[string]any{"filter": "control", "limit": 1})
	if len(first["items"].([]any)) != 1 {
		t.Fatal(first)
	}
	detail := invoke("owner_capabilities", map[string]any{"name": "control.set_mode"})
	if detail["name"] != ownerToolName("control.set_mode") || detail["input_schema"] == nil {
		t.Fatal(detail)
	}
	schema := invoke("owner_capabilities", map[string]any{"schema_type": "SetControlModePayload"})
	if schema["schema"] == nil {
		t.Fatal(schema)
	}
	task := e.task("Actual owner invocation")
	invoke("owner_call", map[string]any{"name": detail["name"], "arguments": protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "task", ID: task}, ExpectedControlRevision: new(int64(1)), Payload: json.RawMessage(`{"mode":"human_control"}`)}})
	if e.count(`SELECT COUNT(*) FROM tasks WHERE task_id='`+task+`' AND control_mode='human_control'`) != 1 {
		t.Fatal("owner command not executed")
	}
}
