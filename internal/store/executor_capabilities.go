package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetCapabilityDefinition(ctx context.Context, q Querier, id string, revision int64) (protocol.CapabilityDefinition, error) {
	var d protocol.CapabilityDefinition
	var body string
	err := q.QueryRowContext(ctx, `SELECT definition_json FROM capability_definition_versions WHERE capability_id=? AND (?=0 OR revision=?) ORDER BY revision DESC LIMIT 1`, id, revision, revision).Scan(&body)
	if err != nil {
		return d, notFound(err, "capability definition")
	}
	err = json.Unmarshal([]byte(body), &d)
	return d, err
}

func (t *Tx) InsertCapabilityDefinition(ctx context.Context, d protocol.CapabilityDefinition, requestID string) error {
	body, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO capability_definition_versions(capability_id,revision,definition_json,request_id) VALUES(?,?,?,?)`, d.CapabilityID, d.Revision, string(body), requestID)
	return err
}

// CapabilityQueryCursor fixes the filter and watermark. A cursor is not an
// authorization token; callers still pass their usual owner/run authorization.
type CapabilityQueryCursor struct {
	After        string `json:"after"`
	AsOf         int64  `json:"as_of"`
	FilterSHA256 string `json:"filter_sha256"`
}

func CapabilityCursor(filter any, after string, current int64) (CapabilityQueryCursor, error) {
	b, err := json.Marshal(filter)
	if err != nil {
		return CapabilityQueryCursor{}, err
	}
	sum := sha256.Sum256(b)
	c := CapabilityQueryCursor{AsOf: current, FilterSHA256: hex.EncodeToString(sum[:])}
	if after == "" {
		return c, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(after)
	var old CapabilityQueryCursor
	if err != nil || len(raw) > 8192 || json.Unmarshal(raw, &old) != nil || old.FilterSHA256 != c.FilterSHA256 || old.AsOf < 1 || old.AsOf > current {
		return c, protocol.Errorf(protocol.CodeInvalidRequest, "invalid capability cursor or changed filters")
	}
	return old, nil
}

func (c CapabilityQueryCursor) Encode(after string) string {
	c.After = after
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func ListCapabilityDefinitions(ctx context.Context, q Querier, id, state, search, after string, limit int) (protocol.CapabilityPage, error) {
	page := protocol.CapabilityPage{Items: []protocol.CapabilityDefinition{}}
	var maxSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM capability_definition_versions`).Scan(&maxSeq); err != nil {
		return page, err
	}
	c, err := CapabilityCursor([]string{"definitions", id, state, search}, after, maxSeq)
	if err != nil {
		return page, err
	}
	page.AsOf = c.AsOf
	lastRevision := int64(0)
	if id != "" && c.After != "" {
		lastRevision, err = strconv.ParseInt(c.After, 10, 64)
		if err != nil {
			return page, protocol.Errorf(protocol.CodeInvalidRequest, "invalid revision cursor")
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT definition_json FROM capability_definition_versions d WHERE d.sequence<=? AND ((?<>'' AND capability_id=? AND revision>?) OR (?='' AND capability_id>? AND revision=(SELECT MAX(v.revision) FROM capability_definition_versions v WHERE v.capability_id=d.capability_id AND v.sequence<=?))) AND (?='all' OR json_extract(definition_json,'$.state')=?) AND (?='' OR instr(lower(json_extract(definition_json,'$.title') || ' ' || capability_id),lower(?))>0) ORDER BY capability_id,revision LIMIT ?`, c.AsOf, id, id, lastRevision, id, c.After, c.AsOf, state, state, search, search, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return page, err
		}
		if len(page.Items) == limit {
			last := page.Items[len(page.Items)-1]
			key := last.CapabilityID
			if id != "" {
				key = strconv.FormatInt(last.Revision, 10)
			}
			page.Next = c.Encode(key)
			break
		}
		var d protocol.CapabilityDefinition
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			return page, err
		}
		page.Items = append(page.Items, d)
	}
	return page, rows.Err()
}

func (t *Tx) InsertRunCapabilityBinding(ctx context.Context, runID string, b protocol.RunCapabilityBinding) error {
	body, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO run_capability_bindings(run_id,binding_json) VALUES(?,?)`, runID, string(body))
	return err
}

func GetRunCapabilityBinding(ctx context.Context, q Querier, runID string) (protocol.RunCapabilityBinding, error) {
	var b protocol.RunCapabilityBinding
	var body string
	err := q.QueryRowContext(ctx, `SELECT binding_json FROM run_capability_bindings WHERE run_id=?`, runID).Scan(&body)
	if err != nil {
		return b, notFound(err, "run capability binding")
	}
	if err = json.Unmarshal([]byte(body), &b); err != nil {
		return b, fmt.Errorf("store: decode run capability binding: %w", err)
	}
	return b, nil
}

// ListAgentCapabilityRevisions uses registration history, not evidence history.
// Watermarks are per environment; repeated registration of an unchanged Agent
// remains visible with its original snapshot and registration revisions.
func ListAgentCapabilityRevisions(ctx context.Context, q Querier, host, agent string, revision int64, after string, limit int) (protocol.AgentCapabilityRevisionPage, error) {
	page := protocol.AgentCapabilityRevisionPage{Items: []protocol.AgentCapabilityRevision{}}
	var maxRevision int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(server_revision),0) FROM agent_inventory_versions WHERE host_id=?`, host).Scan(&maxRevision); err != nil {
		return page, err
	}
	cursor, err := CapabilityCursor([]any{"declaration_revisions", host, agent, revision}, after, maxRevision)
	if err != nil {
		return page, err
	}
	page.AsOf = cursor.AsOf
	last := int64(0)
	if cursor.After != "" {
		last, err = strconv.ParseInt(cursor.After, 10, 64)
		if err != nil || last < 1 {
			return page, protocol.Errorf(protocol.CodeInvalidRequest, "invalid Agent revision cursor")
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT a.value,v.local_revision,v.server_revision,v.snapshot_id,v.public_sha256 FROM agent_inventory_versions v,json_each(v.projection_json,'$.agents') a WHERE v.host_id=? AND v.server_revision>? AND v.server_revision<=? AND json_extract(a.value,'$.identity.local_id')=? AND (?=0 OR json_extract(a.value,'$.revision')=?) ORDER BY v.server_revision LIMIT ?`, host, last, cursor.AsOf, agent, revision, revision, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Items) == limit {
			page.Next = cursor.Encode(strconv.FormatInt(page.Items[len(page.Items)-1].ServerRevision, 10))
			break
		}
		var item protocol.AgentCapabilityRevision
		var body string
		if err := rows.Scan(&body, &item.InventoryRevision, &item.ServerRevision, &item.SnapshotID, &item.PublicSHA256); err != nil {
			return page, err
		}
		if err := json.Unmarshal([]byte(body), &item.Definition); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}
