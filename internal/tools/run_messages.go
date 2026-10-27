package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

type sendRunMessageArgs struct {
	RunID                   string `json:"run_id"`
	TaskID                  string `json:"task_id"`
	SpecVersion             int64  `json:"spec_version"`
	Generation              int64  `json:"generation"`
	ExpectedControlRevision int64  `json:"expected_control_revision"`
	Text                    string `json:"text"`
	CommitSeq               int64  `json:"commit_seq"`
	Reason                  string `json:"reason,omitempty"`
}

func (s *Service) runsSendMessage(ctx context.Context, c Call) (any, error) {
	var a sendRunMessageArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, protocol.OpRunsSendMessage, protocol.Target{Type: "run", ID: a.RunID}, &a.ExpectedControlRevision, a.Reason,
		protocol.SendRunMessagePayload{TaskID: a.TaskID, SpecVersion: a.SpecVersion, Generation: a.Generation, Text: a.Text, SourceMessageID: c.SourceMessageID, CommitSeq: a.CommitSeq}, app.SendRunMessage{})
}

type readRunMessagesArgs struct {
	RunID string `json:"run_id"`
	After int64  `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

func (s *Service) runsReadMessages(ctx context.Context, c Call) (any, error) {
	var a readRunMessagesArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if a.Limit == 0 {
		a.Limit = 20
	}
	return s.app.RunMessages(ctx, c.Principal, a.RunID, a.After, a.Limit, false)
}
