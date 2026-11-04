package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type capabilityListArgs struct {
	State  string `json:"state,omitempty"`
	Search string `json:"search,omitempty"`
	After  string `json:"after,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type capabilityReadArgs struct {
	CapabilityID string `json:"capability_id"`
	Revision     int64  `json:"revision,omitempty"`
}
type taskCapabilityArgs struct {
	Candidates  protocol.AgentCapabilityQuery `json:"candidates,omitzero"`
	TaskID      string                        `json:"task_id"`
	SpecVersion int64                         `json:"spec_version,omitempty"`
	Role        protocol.RunRole              `json:"role,omitempty"`
}

func (s *Service) hostedCapabilities(ctx context.Context, c Call, execution store.HostedExecution) (harness.ToolResult, bool) {
	if c.Tool != "capabilities_list" && c.Tool != "capabilities_read" && c.Tool != "agents_capabilities_list" && c.Tool != "agents_capabilities_read" && c.Tool != "tasks_capability_explain" {
		return harness.ToolResult{}, false
	}
	if execution.Selection.Purpose != agentconfig.Main && execution.Selection.Purpose != agentconfig.IssueManager {
		return hostedAnswer(nil, protocol.Errorf(protocol.CodeForbidden, "capability catalog is limited to the planner and Issue manager")), true
	}
	switch c.Tool {
	case "capabilities_list":
		var p capabilityListArgs
		if err := decode(c.Arguments, &p); err != nil {
			return hostedAnswer(nil, err), true
		}
		v, err := s.app.CapabilityDefinitions(ctx, "", p.State, p.Search, p.After, p.Limit)
		return hostedAnswer(v, err), true
	case "capabilities_read":
		var p capabilityReadArgs
		if err := decode(c.Arguments, &p); err != nil {
			return hostedAnswer(nil, err), true
		}
		var v protocol.CapabilityDefinition
		err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			if !protocol.AgentInventoryID(p.CapabilityID) || p.Revision < 0 {
				return protocol.Errorf(protocol.CodeInvalidRequest, "valid capability_id and nonnegative revision required")
			}
			var err error
			v, err = store.GetCapabilityDefinition(ctx, tx.Q(), p.CapabilityID, p.Revision)
			return err
		})
		return hostedAnswer(v, err), true
	case "agents_capabilities_list", "agents_capabilities_read":
		var p protocol.AgentCapabilityQuery
		if err := decode(c.Arguments, &p); err != nil {
			return hostedAnswer(nil, err), true
		}
		if c.Tool == "agents_capabilities_read" && (p.Environment == "" || p.Agent == "") {
			return hostedAnswer(nil, protocol.Errorf(protocol.CodeInvalidRequest, "exact environment and agent are required")), true
		}
		v, err := s.app.CapabilityAgents(ctx, p)
		return hostedAnswer(v, err), true
	default:
		var p taskCapabilityArgs
		if err := decode(c.Arguments, &p); err != nil {
			return hostedAnswer(nil, err), true
		}
		if execution.Selection.Purpose == agentconfig.IssueManager {
			err := s.store.Read(ctx, func(tx *store.ReadTx) error {
				allowed, err := hostedTaskResultReadable(ctx, tx.Q(), p.TaskID, execution.IssueID)
				if err != nil {
					return err
				}
				if !allowed {
					return protocol.Errorf(protocol.CodeForbidden, "task capability constraints are outside this Issue")
				}
				return nil
			})
			if err != nil {
				return hostedAnswer(nil, err), true
			}
		}
		v, err := s.app.TaskCapabilityExplanationPage(ctx, p.TaskID, p.SpecVersion, p.Role, p.Candidates)
		return hostedAnswer(v, err), true
	}
}
