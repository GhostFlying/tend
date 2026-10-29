package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetAgentCategory(ctx context.Context, q Querier, id string) (protocol.AgentCategory, error) {
	var c protocol.AgentCategory
	err := q.QueryRowContext(ctx, `SELECT category_id,revision,title,description,state FROM agent_category_versions WHERE category_id=? ORDER BY revision DESC LIMIT 1`, id).Scan(&c.CategoryID, &c.Revision, &c.Title, &c.Description, &c.State)
	if err != nil {
		return c, notFound(err, "agent category")
	}
	return c, nil
}

func (t *Tx) InsertAgentCategory(ctx context.Context, c protocol.AgentCategory, requestID string) error {
	_, err := t.Exec(ctx, `INSERT INTO agent_category_versions(category_id,revision,title,description,state,request_id) VALUES(?,?,?,?,?,?)`, c.CategoryID, c.Revision, c.Title, c.Description, c.State, requestID)
	return err
}

type AgentInventoryRow struct {
	Receipt       protocol.AgentInventoryReceipt
	Projection    protocol.AgentInventoryProjection
	CanonicalJSON string
}

func GetAgentInventory(ctx context.Context, q Querier, host string) (AgentInventoryRow, error) {
	var r AgentInventoryRow
	var receipt string
	err := q.QueryRowContext(ctx, `SELECT receipt_json,projection_json FROM agent_inventory_versions WHERE host_id=? ORDER BY server_revision DESC LIMIT 1`, host).Scan(&receipt, &r.CanonicalJSON)
	if err != nil {
		return r, notFound(err, "agent inventory")
	}
	if err := json.Unmarshal([]byte(receipt), &r.Receipt); err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(r.CanonicalJSON), &r.Projection)
	return r, err
}

func (t *Tx) InsertAgentInventory(ctx context.Context, r AgentInventoryRow, runtime protocol.RunnerRuntime) error {
	body, err := json.Marshal(r.Receipt)
	if err != nil {
		return err
	}
	c := r.Receipt
	_, err = t.Exec(ctx, `INSERT INTO agent_inventory_versions(host_id,server_revision,local_revision,snapshot_id,public_sha256,projection_json,receipt_json,instance_id,generation) VALUES(?,?,?,?,?,?,?,?,?)`, c.HostID, c.ServerRevision, c.LocalRevision, c.SnapshotID, c.PublicSHA256, r.CanonicalJSON, string(body), runtime.InstanceID, runtime.Generation)
	return err
}

// ListAgentCategories filters after selecting the latest revision, so retiring
// a category cannot reveal an older active revision in the default listing.
func ListAgentCategories(ctx context.Context, q Querier, state, after string, limit int) (protocol.AgentCategoryPage, error) {
	page := protocol.AgentCategoryPage{Items: []protocol.AgentCategory{}}
	rows, err := q.QueryContext(ctx, `SELECT c.category_id,c.revision,c.title,c.description,c.state FROM agent_category_versions c WHERE c.revision=(SELECT MAX(v.revision) FROM agent_category_versions v WHERE v.category_id=c.category_id) AND (?='all' OR c.state=?) AND c.category_id>? ORDER BY c.category_id LIMIT ?`, state, state, after, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var c protocol.AgentCategory
		if err := rows.Scan(&c.CategoryID, &c.Revision, &c.Title, &c.Description, &c.State); err != nil {
			return page, err
		}
		if len(page.Items) == limit {
			page.More = true
			break
		}
		page.Items = append(page.Items, c)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if page.More {
		page.Next = page.Items[len(page.Items)-1].CategoryID
	}
	return page, nil
}
