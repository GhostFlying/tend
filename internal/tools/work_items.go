package tools

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/protocol/schema"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type workItemsListArgs struct {
	Project       *string `json:"project,omitempty"`
	IncludeClosed bool    `json:"include_closed,omitempty"`
	Column        string  `json:"column,omitempty"`
}
type workItemReadArgs struct {
	WorkItemID string `json:"work_item_id"`
	Revision   int64  `json:"revision,omitempty"`
}

type workItemRefreshArgs struct {
	WorkItemID              string `json:"work_item_id"`
	ExpectedControlRevision int64  `json:"expected_control_revision"`
	DeliveryID              string `json:"delivery_id"`
	Reason                  string `json:"reason"`
}
type workItemCreateArgs struct {
	Plan      protocol.WorkItemPlan `json:"plan"`
	CommitSeq int64                 `json:"commit_seq"`
	Reason    string                `json:"reason"`
}
type workItemUpdateArgs struct {
	WorkItemID              string                             `json:"work_item_id"`
	ExpectedControlRevision int64                              `json:"expected_control_revision"`
	Plan                    protocol.WorkItemPlan              `json:"plan"`
	Status                  protocol.WorkItemUpdateStatus      `json:"status,omitempty"`
	TaskDispositions        []protocol.WorkItemTaskDisposition `json:"task_dispositions,omitempty"`
	CommitSeq               int64                              `json:"commit_seq"`
	Reason                  string                             `json:"reason"`
}
type workItemCloseArgs struct {
	CommitSeq               int64                    `json:"commit_seq"`
	WorkItemID              string                   `json:"work_item_id"`
	ExpectedControlRevision int64                    `json:"expected_control_revision"`
	Closure                 protocol.WorkItemClosure `json:"closure"`
	Reason                  string                   `json:"reason"`
}
type workItemReopenArgs struct {
	CommitSeq               int64  `json:"commit_seq"`
	WorkItemID              string `json:"work_item_id"`
	ExpectedControlRevision int64  `json:"expected_control_revision"`
	Reason                  string `json:"reason"`
}
type workItemLinkArgs struct {
	CommitSeq               int64    `json:"commit_seq"`
	WorkItemID              string   `json:"work_item_id"`
	ExpectedControlRevision int64    `json:"expected_control_revision"`
	TaskID                  string   `json:"task_id"`
	StageIDs                []string `json:"stage_ids"`
	Reason                  string   `json:"reason"`
}

// argumentSchema derives the larger planning contract from the same types as
// the owner API. Keep the actual root at the top level for MCP consumers.
func argumentSchema(v any) string {
	g := schema.New()
	for _, values := range protocol.APIEnums() {
		g.Enum(values)
	}
	g.Add(v)
	b, err := g.Document("tool input")
	if err != nil {
		panic(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		panic(err)
	}
	defs := doc["definitions"].(map[string]any)
	root := defs[reflect.TypeOf(v).Name()].(map[string]any)
	delete(defs, reflect.TypeOf(v).Name())
	// Copy before attaching definitions: a shared root would be self-referential.
	copyRoot := make(map[string]any, len(root))
	for k, value := range root {
		if k != "definitions" {
			copyRoot[k] = value
		}
	}
	delete(root, "definitions")
	copyRoot["definitions"] = defs
	b, err = json.Marshal(copyRoot)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (s *Service) workItemsList(ctx context.Context, c Call) (any, error) {
	var a workItemsListArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	var page protocol.WorkItemPage
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		project := ""
		if a.Project == nil {
			var err error
			project, err = app.SessionScope(ctx, tx.Q(), c.Principal)
			if err != nil {
				return err
			}
		} else if *a.Project != "*" {
			project = *a.Project
		}
		var err error
		page, err = views.WorkItems(ctx, tx.Q(), project, a.IncludeClosed, a.Column)
		return err
	})
	return page, err
}
func (s *Service) workItemsRead(ctx context.Context, c Call) (any, error) {
	var a workItemReadArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	var item protocol.WorkItemView
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		if a.Revision > 0 {
			w, err := store.GetWorkItemRevision(ctx, tx.Q(), a.WorkItemID, a.Revision)
			item = protocol.WorkItemView{WorkItem: w, Column: "historical"}
			return notFound(err, "work item revision does not exist")
		}
		var err error
		item, err = views.WorkItem(ctx, tx.Q(), a.WorkItemID)
		return notFound(err, "work item does not exist")
	})
	return item, err
}

func (s *Service) workItemsRefresh(ctx context.Context, c Call) (any, error) {
	var a workItemRefreshArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, protocol.OpRefreshDelivery, protocol.Target{Type: "work_item", ID: a.WorkItemID}, &a.ExpectedControlRevision, a.Reason, protocol.RefreshDeliveryPayload{DeliveryID: a.DeliveryID}, app.RefreshDelivery{})
}
func (s *Service) workItemsCreate(ctx context.Context, c Call) (any, error) {
	var a workItemCreateArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	p := protocol.CreateWorkItemPayload{WorkItemPlan: a.Plan, SourceMessageID: c.SourceMessageID, CommitSeq: a.CommitSeq}
	return s.execute(ctx, c, "work_items.create", protocol.Target{Type: "work_item"}, nil, a.Reason, p, app.CreateWorkItem{})
}
func (s *Service) workItemsUpdate(ctx context.Context, c Call) (any, error) {
	var a workItemUpdateArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	p := protocol.UpdateWorkItemPayload{WorkItemPlan: a.Plan, Status: a.Status, TaskDispositions: a.TaskDispositions, SourceMessageID: c.SourceMessageID, CommitSeq: a.CommitSeq}
	return s.execute(ctx, c, "work_items.update", protocol.Target{Type: "work_item", ID: a.WorkItemID}, &a.ExpectedControlRevision, a.Reason, p, app.UpdateWorkItem{})
}
func (s *Service) workItemsClose(ctx context.Context, c Call) (any, error) {
	var a workItemCloseArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "work_items.close", protocol.Target{Type: "work_item", ID: a.WorkItemID}, &a.ExpectedControlRevision, a.Reason, protocol.CloseWorkItemPayload{Closure: a.Closure, SourceMessageID: c.SourceMessageID, CommitSeq: a.CommitSeq}, app.CloseWorkItem{})
}
func (s *Service) workItemsReopen(ctx context.Context, c Call) (any, error) {
	var a workItemReopenArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "work_items.reopen", protocol.Target{Type: "work_item", ID: a.WorkItemID}, &a.ExpectedControlRevision, a.Reason, protocol.ReopenWorkItemPayload{Reason: a.Reason, SourceMessageID: c.SourceMessageID, CommitSeq: a.CommitSeq}, app.ReopenWorkItem{})
}
func (s *Service) workItemsLink(ctx context.Context, c Call) (any, error) {
	var a workItemLinkArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	if err := requireMessage(c); err != nil {
		return nil, err
	}
	return s.execute(ctx, c, "work_items.link_task", protocol.Target{Type: "work_item", ID: a.WorkItemID}, &a.ExpectedControlRevision, a.Reason, protocol.LinkWorkItemTaskPayload{TaskID: a.TaskID, StageIDs: a.StageIDs, SourceMessageID: c.SourceMessageID, CommitSeq: a.CommitSeq}, app.LinkWorkItemTask{})
}
