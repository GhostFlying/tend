package api

import (
	"encoding/base64"
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
	if issue.AcceptancePolicy.Creator.Kind != "agent" || issue.AcceptancePolicy.Creator.Agent == nil || issue.AcceptancePolicy.Creator.OwnerSubject != ownerLogin {
		t.Fatal("creator provenance replaced by represented owner", issue.AcceptancePolicy)
	}
	replay, err := ownerToolInvoke(t, e, call, "issues.create", args)
	if err != nil || string(replay.Operation.Result) != string(response.Operation.Result) {
		t.Fatal(replay, err)
	}
	if n := e.count("SELECT COUNT(*) FROM work_items"); n != 1 {
		t.Fatal("duplicate create", n)
	}
	err = e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		allowed, err := store.HasOwnerRequestAuthority(t.Context(), tx.Q(), call.RequestID)
		if err != nil {
			return err
		}
		if !allowed {
			t.Fatal("delegated request lost owner authority")
		}
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
	status, _ := json.Marshal(protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueTodo, Owner: &protocol.IssueOwnerRequest{CategoryID: "planner", Reason: "Explicit immediate execution"}})
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
	var namedReceipt struct {
		Result struct {
			Outcome protocol.Issue `json:"outcome"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result.Text), &namedReceipt); err != nil || namedReceipt.Result.Outcome.IssueID == "" {
		t.Fatal("named tool lost its concrete outcome", result.Text, err)
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

func TestMainOwnerDecisionAndMessageProvenance(t *testing.T) {
	e, call := mainOwnerFixture(t)
	var opened struct {
		DecisionID string `json:"decision_id"`
	}
	if err := json.Unmarshal(e.exec("decisions.open", protocol.Target{Type: "decision"}, nil, map[string]any{"question": "Which?", "options": []string{"a", "b"}}, app.OpenDecision{}), &opened); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(protocol.AnswerDecisionPayload{Version: 1, Option: "a", Text: "Main Agent judgment"})
	response, err := ownerToolInvoke(t, e, call, "decisions.respond", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "decision", ID: opened.DecisionID}, Payload: payload})
	if err != nil {
		t.Fatal(response, err)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		decision, err := store.GetDecision(t.Context(), tx.Q(), opened.DecisionID)
		if err != nil {
			return err
		}
		message, err := store.GetMessage(t.Context(), tx.Q(), decision.AnsweredMessageID)
		if err != nil {
			return err
		}
		var answer struct {
			Actor        protocol.Principal `json:"actor"`
			OwnerSubject string             `json:"owner_subject"`
		}
		if err := json.Unmarshal([]byte(decision.AnswerJSON), &answer); err != nil {
			return err
		}
		if message.Author != "agent" || answer.Actor.Kind != protocol.PrincipalMainAgent || answer.OwnerSubject != ownerLogin {
			t.Fatal(message, decision.AnswerJSON)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(protocol.SubmitMessagePayload{Body: "Agent-originated instruction"})
	response, err = ownerToolInvoke(t, e, call, "conversation.submit_message", protocol.OwnerToolArguments{EffectSeq: 2, Target: protocol.Target{Type: "conversation"}, Payload: payload})
	if err != nil {
		t.Fatal(response, err)
	}
	var sent struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(response.Operation.Result, &sent); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		message, err := store.GetMessage(t.Context(), tx.Q(), sent.MessageID)
		if err == nil && message.Author != "agent" {
			t.Fatal(message)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMainOwnerReadsExactOriginalBytesAndRanges(t *testing.T) {
	e, call := mainOwnerFixture(t)
	original := `{ "token": "original document field", "fact": true }` + "\n"
	op, err := e.app.UploadConversationAttachment(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}, e.ids.New(protocol.PrefixRequest), "original.json", "application/json", strings.NewReader(original))
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(op.Result, &file); err != nil {
		t.Fatal(err)
	}
	call.Name = ownerToolName("GET /api/v1/conversation/attachments/{id}")
	for _, tc := range []struct{ window, want string }{{"", original}, {"bytes=2-5", original[2:6]}} {
		call.Arguments, _ = json.Marshal(protocol.OwnerToolArguments{Path: map[string]string{"id": file.FileID}, Range: tc.window})
		result, err := e.server.MainOwnerTools().Call(t.Context(), call)
		if err != nil {
			t.Fatal(result, err)
		}
		value := result.(map[string]any)
		raw, err := base64.StdEncoding.DecodeString(value["content_base64"].(string))
		if err != nil || string(raw) != tc.want || value["content_sha256"] != file.SHA256 {
			t.Fatal(value, err)
		}
	}
}

func TestMainOwnerErrorRetainsOriginalRequestID(t *testing.T) {
	e, call := mainOwnerFixture(t)
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER fail_issue_insert BEFORE INSERT ON work_items BEGIN SELECT RAISE(ABORT,'injected write failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	call.RequestID = e.ids.New(protocol.PrefixRequest)
	call.Name = ownerToolName("issues.create")
	payload, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Injected failure", Project: "default", Intent: "Record", Stages: []protocol.IssueStage{}}})
	call.Arguments, _ = json.Marshal(protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "issue"}, Payload: payload})
	result, err := e.server.MainOwnerTools().Call(t.Context(), call)
	if protocol.CodeOf(err) != protocol.CodeInternal || result.(map[string]any)["request_id"] != call.RequestID {
		t.Fatal(result, err)
	}
}

func TestMainOwnerNamedMutationPreservesReason(t *testing.T) {
	e, call := mainOwnerFixture(t)
	payload, _ := json.Marshal(protocol.CreateAttentionPayload{Kind: "information", Title: "Recorded information"})
	response, err := ownerToolInvoke(t, e, call, "attentions.create", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "attention"}, Payload: payload})
	if err != nil {
		t.Fatal(response, err)
	}
	var attention protocol.Attention
	if err := json.Unmarshal(response.Operation.Result, &attention); err != nil {
		t.Fatal(err)
	}
	if attention.RaisedBy.Kind != protocol.PrincipalMainAgent {
		t.Fatal(attention)
	}
	service := tools.New(e.app, e.store, e.context, quiet())
	service.SetOwnerTools(e.server.MainOwnerTools())
	args, _ := json.Marshal(map[string]any{"target_id": attention.AttentionID, "expected_control_revision": attention.Revision, "reason": "Recorded item is superseded", "effect_seq": 2, "payload": map[string]any{}})
	result := service.Handle(t.Context(), tools.Call{Principal: call.Actor, ExecutionRequestID: call.ExecutionID, TurnID: "turn", Native: "withdraw-call", Tool: "attentions_withdraw", Arguments: args})
	if !result.Success {
		t.Fatal(result.Text)
	}
}

func TestMainOwnerMessageAuthorityCannotBeSpoofedByRequestID(t *testing.T) {
	e, call := mainOwnerFixture(t)
	payload, _ := json.Marshal(protocol.SubmitMessagePayload{Body: "Act for owner"})
	response, err := ownerToolInvoke(t, e, call, "conversation.submit_message", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "conversation"}, Payload: payload})
	if err != nil {
		t.Fatal(response, err)
	}
	var original struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(response.Operation.Result, &original); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		copied := e.ids.New(protocol.PrefixMessage)
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: copied, Author: "agent", Body: "Scoped Agent narration", RequestID: response.Operation.RequestID, CreatedAt: e.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		for id, want := range map[string]bool{original.MessageID: true, copied: false} {
			allowed, err := store.HasOwnerMessageAuthority(t.Context(), tx.Q(), id)
			if err != nil {
				return err
			}
			if allowed != want {
				t.Fatal(id, allowed, want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMainOwnerRecoversUnrecordedRequestInOriginalDomain(t *testing.T) {
	for _, domain := range []string{"owner", "native"} {
		t.Run(domain, func(t *testing.T) {
			e, call := mainOwnerFixture(t)
			payload, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Recover same request", Project: "default", Intent: "Record", Stages: []protocol.IssueStage{}}})
			original := protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "issues.create", Target: protocol.Target{Type: "issue"}, Payload: payload}
			wantDomain := "owner"
			if domain == "native" {
				var err error
				original.RequestID, err = e.app.NativeRequestID(t.Context(), "harness:"+call.Actor.Subject, "original-unrecorded-call")
				if err != nil {
					t.Fatal(err)
				}
				wantDomain = call.Actor.DedupDomain()
			}
			response, err := ownerToolInvoke(t, e, call, "issues.create", protocol.OwnerToolArguments{OriginalEnvelope: &original, EffectSeq: 1})
			if err != nil || response.Operation.RequestID != original.RequestID {
				t.Fatal(response, err)
			}
			if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
				record, err := store.GetRequest(t.Context(), tx.Q(), wantDomain, original.RequestID)
				if err != nil {
					return err
				}
				var actor protocol.Principal
				if err := json.Unmarshal([]byte(record.PrincipalJSON), &actor); err != nil {
					return err
				}
				if actor.Kind != protocol.PrincipalMainAgent {
					t.Fatal("recovery fabricated a human caller", actor)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			again, err := ownerToolInvoke(t, e, call, "issues.create", protocol.OwnerToolArguments{OriginalEnvelope: &original})
			if err != nil || again.Operation.RequestID != original.RequestID || e.count(`SELECT COUNT(*) FROM work_items`) != 1 {
				t.Fatal(again, err)
			}
		})
	}
}

func TestMainOwnerRecoveredIssueKeepsOriginalReceiptDomain(t *testing.T) {
	e, call := mainOwnerFixture(t)
	payload, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Recovered", Project: "default", Intent: "Report", Stages: []protocol.IssueStage{}}})
	original := protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "issues.create", Target: protocol.Target{Type: "issue"}, Payload: payload}
	response, err := ownerToolInvoke(t, e, call, "issues.create", protocol.OwnerToolArguments{OriginalEnvelope: &original, EffectSeq: 1})
	if err != nil {
		t.Fatal(err)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(response.Operation.Result, &issue); err != nil {
		t.Fatal(err)
	}
	status, err := e.app.IssueReuseStatus(t.Context(), issue.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	if status.OriginObservation != "recorded" || status.OriginalRequestID != original.RequestID {
		t.Fatalf("committed creation lost receipt identity: %+v", status)
	}
}

func TestMainOwnerDecisionAnswerDoesNotWakeItself(t *testing.T) {
	e, call := mainOwnerFixture(t)
	var opened struct {
		DecisionID string `json:"decision_id"`
	}
	if err := json.Unmarshal(e.exec("decisions.open", protocol.Target{Type: "decision"}, nil, map[string]any{"question": "Which?", "options": []string{"a", "b"}}, app.OpenDecision{}), &opened); err != nil {
		t.Fatal(err)
	}
	before := e.count(`SELECT COUNT(*) FROM outbox WHERE destination='main_thread'`)
	payload, _ := json.Marshal(protocol.AnswerDecisionPayload{Version: 1, Option: "a"})
	response, err := ownerToolInvoke(t, e, call, "decisions.respond", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "decision", ID: opened.DecisionID}, Payload: payload})
	if err != nil {
		t.Fatal(response, err)
	}
	if after := e.count(`SELECT COUNT(*) FROM outbox WHERE destination='main_thread'`); after != before {
		t.Fatalf("own answer queued %d additional turns", after-before)
	}
}

func TestMainOwnerManualSourceKeepsActualSupplier(t *testing.T) {
	e, call := mainOwnerFixture(t)
	payload, _ := json.Marshal(protocol.InjectSourcePayload{Text: "External evidence"})
	response, err := ownerToolInvoke(t, e, call, "conversation.inject_source", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "conversation"}, Payload: payload})
	if err != nil {
		t.Fatal(response, err)
	}
	var source protocol.ManualSource
	if err := json.Unmarshal(response.Operation.Result, &source); err != nil {
		t.Fatal(err)
	}
	if source.SuppliedBy.Kind != protocol.PrincipalMainAgent {
		t.Fatalf("supplier=%+v", source.SuppliedBy)
	}
}

func TestMainOwnerNotificationRetainsAgentAuthorAndExecution(t *testing.T) {
	e, call := mainOwnerFixture(t)
	payload, _ := json.Marshal(protocol.MessagingPublication{Body: "Agent proactive update", Purpose: "progress", Reason: "New progress"})
	response, err := ownerToolInvoke(t, e, call, "messaging.notify", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "message"}, Payload: payload})
	if err != nil {
		t.Fatal(response, err)
	}
	var result struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(response.Operation.Result, &result); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		m, err := store.GetMessage(t.Context(), tx.Q(), result.MessageID)
		if err != nil {
			return err
		}
		if m.Author != "agent" {
			t.Fatal(m)
		}
		var execution string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT execution_id FROM messaging_messages WHERE message_id=?`, m.MessageID).Scan(&execution); err != nil {
			return err
		}
		if execution != call.ExecutionID {
			t.Fatal("notification lost native execution", execution, call)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMainOwnerReceiptReplaysThroughOwnerAPIWithoutDuplicate(t *testing.T) {
	e, call := mainOwnerFixture(t)
	payload, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Original main request", Project: "default", Intent: "Record", Stages: []protocol.IssueStage{}}})
	call.RequestID = e.ids.New(protocol.PrefixRequest)
	response, err := ownerToolInvoke(t, e, call, "issues.create", protocol.OwnerToolArguments{EffectSeq: 1, Target: protocol.Target{Type: "issue"}, Payload: payload})
	if err != nil {
		t.Fatal(response, err)
	}
	original := protocol.Envelope{RequestID: call.RequestID, Operation: "issues.create", Target: protocol.Target{Type: "issue"}, Payload: payload}
	var replay protocol.APIResponse
	if status := e.do("POST", "/api/v1/ops", e.login(protocol.ScopeOwner), original, &replay); status != 200 || replay.Operation == nil || replay.Operation.Error != nil {
		t.Fatal(status, replay)
	}
	var initialIssue, replayedIssue protocol.Issue
	if err := json.Unmarshal(response.Operation.Result, &initialIssue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replay.Operation.Result, &replayedIssue); err != nil {
		t.Fatal(err)
	}
	if n := e.count("SELECT COUNT(*) FROM work_items"); n != 1 || replayedIssue.IssueID != initialIssue.IssueID || replay.Operation.RequestID != response.Operation.RequestID {
		t.Fatal("owner replay duplicated or changed original receipt", n, replay)
	}
	original.Payload = json.RawMessage(`{"plan":{"title":"Changed"}}`)
	if status := e.do("POST", "/api/v1/ops", e.login(protocol.ScopeOwner), original, &replay); status == 200 {
		t.Fatal("replay ignored changed payload")
	}
}
