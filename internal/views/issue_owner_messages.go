package views

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// OwnerIssueAssociations maps only display metadata; no new responsibility,
// source admission or authorization is created by reading the projection.
func OwnerIssueAssociations(ctx context.Context, q store.Querier, v protocol.MessageAssociations) (protocol.IssueMessageAssociations, error) {
	out := protocol.IssueMessageAssociations{MessageAssociationState: v.MessageAssociationState, Items: []protocol.IssueMessageAssociation{}}
	for _, item := range v.Items {
		canonical, err := ownerCanonicalIdentity(ctx, q, item.WorkItemID)
		if err != nil {
			return out, err
		}
		if !canonical {
			continue
		}
		out.Items = append(out.Items, protocol.IssueMessageAssociation{IssueID: item.WorkItemID, Title: item.Title, Source: item.Source, OwnerLocked: item.OwnerLocked})
	}
	return out, nil
}
func OwnerIssueMessages(ctx context.Context, q store.Querier, items []protocol.ConversationItem) ([]protocol.IssueConversationItem, error) {
	out := make([]protocol.IssueConversationItem, len(items))
	for i, v := range items {
		item := protocol.IssueConversationItem{ConversationContent: v.ConversationContent, Recovery: v.Recovery, ConversationID: v.ConversationID, ReturnLink: v.ReturnLink, Control: v.Control, Choices: v.Choices}
		if v.Associations != nil {
			a, err := OwnerIssueAssociations(ctx, q, *v.Associations)
			if err != nil {
				return nil, err
			}
			item.Associations = &a
		}
		if v.Background != nil {
			id := ""
			canonical, err := ownerCanonicalIdentity(ctx, q, v.Background.WorkItemID)
			if err != nil {
				return nil, err
			}
			if canonical {
				id = v.Background.WorkItemID
			}
			item.Background = &protocol.IssueBackgroundBatch{BackgroundBatchState: v.Background.BackgroundBatchState, IssueID: id}
		}
		out[i] = item
	}
	return out, nil
}
func OwnerIssueConversation(ctx context.Context, q store.Querier, v protocol.AgentConversation) (protocol.IssueAgentConversation, error) {
	out := protocol.IssueAgentConversation{AgentConversationState: v.AgentConversationState}
	canonical, err := ownerCanonicalIdentity(ctx, q, v.WorkItemID)
	if err != nil {
		return out, err
	}
	if canonical {
		out.IssueID = v.WorkItemID
	}
	return out, nil
}
func ownerCanonicalIdentity(ctx context.Context, q store.Querier, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, id).Scan(&exists)
	return exists, err
}

func OwnerIssueQueue(ctx context.Context, q store.Querier, v protocol.ConversationQueuePage) (protocol.IssueConversationQueuePage, error) {
	out := protocol.IssueConversationQueuePage{Enabled: v.Enabled, EventSeq: v.EventSeq, Total: v.Total, Next: v.Next, Items: make([]protocol.IssueConversationQueueEntry, len(v.Items))}
	entry := func(value protocol.ConversationQueueEntry) (protocol.IssueConversationQueueEntry, error) {
		items, err := OwnerIssueMessages(ctx, q, []protocol.ConversationItem{value.Item})
		if err != nil {
			return protocol.IssueConversationQueueEntry{}, err
		}
		return protocol.IssueConversationQueueEntry{Item: items[0], Position: value.Position, Revision: value.Revision, Status: value.Status, CanWithdraw: value.CanWithdraw}, nil
	}
	for i, value := range v.Items {
		item, err := entry(value)
		if err != nil {
			return out, err
		}
		out.Items[i] = item
	}
	if v.Active != nil {
		item, err := entry(*v.Active)
		if err != nil {
			return out, err
		}
		out.Active = &item
	}
	return out, nil
}
func OwnerIssueTranscriptBlock(ctx context.Context, q store.Querier, v protocol.TranscriptBlock) (protocol.IssueTranscriptBlock, error) {
	out := protocol.IssueTranscriptBlock{NoticeOnly: v.NoticeOnly, Position: v.Position, More: v.More, NextSeq: v.NextSeq}
	trigger, err := OwnerIssueMessages(ctx, q, []protocol.ConversationItem{v.Trigger})
	if err != nil {
		return out, err
	}
	out.Trigger = trigger[0]
	out.Items, err = OwnerIssueMessages(ctx, q, v.Items)
	return out, err
}
func OwnerIssueTranscript(ctx context.Context, q store.Querier, v protocol.TranscriptPage) (protocol.IssueTranscriptPage, error) {
	out := protocol.IssueTranscriptPage{More: v.More, NextBefore: v.NextBefore, EventSeq: v.EventSeq, Blocks: make([]protocol.IssueTranscriptBlock, len(v.Blocks))}
	for i, value := range v.Blocks {
		block, err := OwnerIssueTranscriptBlock(ctx, q, value)
		if err != nil {
			return out, err
		}
		out.Blocks[i] = block
	}
	return out, nil
}

// OwnerIssueMessage reads original material by its actual message identity even
// before it has a transcript block. It does not admit input to any agent turn.
func OwnerIssueMessage(ctx context.Context, q store.Querier, id string) (protocol.IssueConversationItem, error) {
	row, err := store.GetConversationItem(ctx, q, id)
	if err != nil {
		return protocol.IssueConversationItem{}, err
	}
	item, err := conversationItem(ctx, q, row)
	if err != nil {
		return protocol.IssueConversationItem{}, err
	}
	items, err := OwnerIssueMessages(ctx, q, []protocol.ConversationItem{item})
	if err != nil {
		return protocol.IssueConversationItem{}, err
	}
	return items[0], nil
}
