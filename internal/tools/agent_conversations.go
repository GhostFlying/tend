package tools

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type turnReadArgs struct{}

type associateArgs struct {
	Disposition     string   `json:"disposition"`
	ContentRevision int64    `json:"content_revision"`
	MessageID       string   `json:"message_id"`
	Revision        int64    `json:"expected_control_revision"`
	WorkItemIDs     []string `json:"work_item_ids"`
}
type contextReadArgs struct {
	WorkItemID string `json:"work_item_id"`
	After      int64  `json:"after,omitempty"`
}
type contextPrepareArgs struct {
	RetiredFactIDs   []string `json:"retired_fact_ids,omitempty"`
	ConfirmedFactIDs []string `json:"confirmed_fact_ids,omitempty"`
	WorkItemID       string   `json:"work_item_id"`
	Revision         int64    `json:"expected_control_revision"`
	Coverage         string   `json:"coverage"`
	Gaps             string   `json:"gaps"`
}
type contextFactArgs struct {
	WorkItemID string               `json:"work_item_id"`
	Revision   int64                `json:"expected_control_revision"`
	Fact       protocol.ContextFact `json:"fact"`
}
type agentListArgs struct {
	Search string `json:"search,omitempty"`
	Status string `json:"status,omitempty"`
	After  string `json:"after,omitempty"`
}
type rolloutArgs struct {
	RecordSeq      int64  `json:"record_seq,omitempty"`
	Offset         int64  `json:"offset,omitempty"`
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id,omitempty"`
	After          int64  `json:"after,omitempty"`
}

func (s *Service) associate(ctx context.Context, c Call) (any, error) {
	var p associateArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	cmd := app.SetMessageAssociations{}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "message", ID: p.MessageID}, &p.Revision, "", protocol.SetMessageAssociationsPayload{WorkItemIDs: p.WorkItemIDs, Disposition: p.Disposition, ContentRevision: p.ContentRevision}, cmd)
}
func (s *Service) contextRead(ctx context.Context, c Call) (any, error) {
	var p contextReadArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	if p.After < 0 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid cursor")
	}
	var v protocol.WorkItemContext
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		if err := backgroundScope(ctx, tx.Q(), c, p.WorkItemID); err != nil {
			return err
		}
		var err error
		v, err = views.WorkItemContext(ctx, tx.Q(), p.WorkItemID, p.After)
		return err
	})
	return v, err
}
func (s *Service) contextPrepare(ctx context.Context, c Call) (any, error) {
	var p contextPrepareArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	cmd := app.ContextCommand{Action: "prepare"}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "work_item", ID: p.WorkItemID}, &p.Revision, "", protocol.PrepareWorkItemContextPayload{Coverage: p.Coverage, Gaps: p.Gaps, RetiredFactIDs: p.RetiredFactIDs, ConfirmedFactIDs: p.ConfirmedFactIDs}, cmd)
}
func (s *Service) contextFact(ctx context.Context, c Call) (any, error) {
	var p contextFactArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	cmd := app.ContextCommand{Action: "fact"}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "work_item", ID: p.WorkItemID}, &p.Revision, "", p.Fact, cmd)
}
func (s *Service) agentList(ctx context.Context, c Call) (any, error) {
	var p agentListArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	var v protocol.AgentConversationPage
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		v, err = store.AgentConversations(ctx, tx.Q(), p.Search, p.Status, p.After, 30)
		// Hosted main-agent startup always enables the background pool (D114).
		// This is capability availability, not the state of an individual turn.
		v.RuntimeAvailable = c.Principal.Kind == protocol.PrincipalMainAgent && c.Principal.Validate() == nil
		return err
	})
	return v, err
}
func (s *Service) rolloutRead(ctx context.Context, c Call) (any, error) {
	var p rolloutArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	var v any
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		conv, err := store.AgentConversation(ctx, tx.Q(), p.ConversationID)
		if err != nil {
			return err
		}
		if err = backgroundScope(ctx, tx.Q(), c, conv.WorkItemID); err != nil {
			return err
		}
		if p.RecordSeq != 0 {
			v, err = store.AgentRolloutWindow(ctx, tx.Q(), conv.ID, p.RecordSeq, p.Offset)
		} else {
			v, err = store.AgentRollout(ctx, tx.Q(), conv.ID, p.TurnID, p.After)
		}
		return err
	})
	return v, err
}
func backgroundScope(ctx context.Context, q store.Querier, c Call, item string) error {
	if c.Principal.Kind != protocol.PrincipalBackgroundAgent {
		return nil
	}
	conv, err := store.AgentConversation(ctx, q, c.Principal.ConversationID)
	if err != nil {
		return err
	}
	if item == "" || conv.WorkItemID != item {
		return protocol.Errorf(protocol.CodeForbidden, "evidence is outside this background conversation")
	}
	return nil
}
func (s *Service) backgroundItemRead(ctx context.Context, c Call) (any, error) {
	var p contextReadArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	var v protocol.WorkItemView
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		if err := backgroundScope(ctx, tx.Q(), c, p.WorkItemID); err != nil {
			return err
		}
		var err error
		v, err = views.WorkItem(ctx, tx.Q(), p.WorkItemID)
		return err
	})
	return v, err
}
func (s *Service) backgroundSubmit(ctx context.Context, c Call) (any, error) {
	var p protocol.BackgroundAssessmentPayload
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	cmd := app.SubmitBackgroundAssessment{MessageID: c.SourceMessageID, TurnID: c.TurnID, DeliveryID: c.ParentRequestID}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "conversation", ID: c.Principal.ConversationID}, nil, "", p, cmd)
}

type evidenceArgs struct {
	MessageID  string `json:"message_id,omitempty"`
	FileID     string `json:"file_id,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
	Offset     int64  `json:"offset"`
}

func (s *Service) backgroundEvidence(ctx context.Context, c Call) (any, error) {
	var p evidenceArgs
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	if p.Offset < 0 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid evidence offset")
	}
	var out any
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		conv, err := store.AgentConversation(ctx, tx.Q(), c.Principal.ConversationID)
		if err != nil {
			return err
		}
		if p.ArtifactID != "" {
			var linked bool
			if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifacts a JOIN work_item_task_links l ON l.task_id=a.task_id WHERE a.artifact_id=? AND l.work_item_id=?)`, p.ArtifactID, conv.WorkItemID).Scan(&linked); err != nil {
				return err
			}
			if !linked {
				return protocol.Errorf(protocol.CodeForbidden, "artifact outside work item")
			}
			artifact, err := views.Artifact(ctx, tx.Q(), p.ArtifactID)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(artifact)
			if err != nil {
				return err
			}
			runes := []rune(string(raw))
			start := min(p.Offset, int64(len(runes)))
			end := min(start+16000, int64(len(runes)))
			out = map[string]any{"text": string(runes[start:end]), "next": end, "more": end < int64(len(runes))}
			return nil
		}
		var linked bool
		if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_work_items WHERE message_id=? AND work_item_id=? AND active=1)`, p.MessageID, conv.WorkItemID).Scan(&linked); err != nil {
			return err
		}
		if !linked {
			return protocol.Errorf(protocol.CodeForbidden, "message outside work item")
		}
		msg, err := store.GetMessage(ctx, tx.Q(), p.MessageID)
		if err != nil {
			return err
		}
		visible := protocol.ConversationItem{MessageID: msg.MessageID, Author: msg.Author, Body: msg.Body}
		if err = store.HideUndeliveredOwner(ctx, tx.Q(), &visible); err != nil {
			return err
		}
		if visible.Body != msg.Body {
			return protocol.Errorf(protocol.CodeForbidden, "source is not admitted")
		}
		if p.FileID != "" {
			if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_message_attachments WHERE message_id=? AND file_id=?)`, p.MessageID, p.FileID).Scan(&linked); err != nil {
				return err
			}
			if !linked {
				return protocol.Errorf(protocol.CodeForbidden, "attachment is not linked to source")
			}
			return nil
		}
		runes := []rune(msg.Body)
		start := min(p.Offset, int64(len(runes)))
		end := min(start+16000, int64(len(runes)))
		out = map[string]any{"text": string(runes[start:end]), "next": end, "more": end < int64(len(runes))}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if p.FileID != "" && p.ArtifactID == "" {
		args, _ := json.Marshal(fileArgs{FileID: p.FileID, Offset: p.Offset})
		c.Arguments = args
		value, err := s.attachmentsRead(ctx, c)
		if err != nil {
			return nil, err
		}
		window := value.(attachmentWindow)
		window.DownloadCommand = ""
		window.LocalPath = ""
		return window, nil
	}
	return out, nil
}

func (s *Service) turnRead(ctx context.Context, c Call) (any, error) {
	return s.app.LogicalTurn(ctx, c.executionRequest())
}
func (s *Service) turnCommit(ctx context.Context, c Call) (any, error) {
	var p protocol.LogicalTurnCommit
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	cmd := app.CommitLogicalTurn{RequestID: c.executionRequest()}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "logical_turn", ID: c.executionRequest()}, nil, "", p, cmd)
}

func (s *Service) turnWait(ctx context.Context, c Call) (any, error) {
	var p protocol.TurnClarification
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	cmd := app.AwaitTurnClarification{RequestID: c.executionRequest()}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "logical_turn", ID: c.executionRequest()}, nil, "", p, cmd)
}

func (s *Service) turnAdopt(ctx context.Context, c Call) (any, error) {
	var p protocol.AdoptClarificationPayload
	if err := decode(c.Arguments, &p); err != nil {
		return nil, err
	}
	cmd := app.AdoptClarification{RequestID: c.executionRequest()}
	return s.execute(ctx, c, cmd.Operation(), protocol.Target{Type: "logical_turn", ID: p.LogicalTurnID}, &p.Revision, "", p, cmd)
}
