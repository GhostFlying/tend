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

func (t *Tx) InsertCapabilityEvidence(ctx context.Context, e protocol.ExecutorCapabilityEvidence) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO executor_capability_evidence(evidence_id,host_id,agent_id,capability_id,definition_revision,evidence_json) VALUES(?,?,?,?,?,?)`, e.EvidenceID, e.Pin.Identity.ExecutionEnvironmentID, e.Pin.Identity.LocalID, e.Capability.CapabilityID, e.Capability.DefinitionRevision, string(body))
	return err
}

func GetCapabilityEvidence(ctx context.Context, q Querier, id string) (protocol.ExecutorCapabilityEvidence, error) {
	var e protocol.ExecutorCapabilityEvidence
	var body string
	err := q.QueryRowContext(ctx, `SELECT evidence_json FROM executor_capability_evidence WHERE evidence_id=?`, id).Scan(&body)
	if err != nil {
		return e, notFound(err, "capability evidence")
	}
	err = json.Unmarshal([]byte(body), &e)
	return e, err
}

type CapabilityEvidenceQuery struct {
	Host       string
	Agent      string
	Capability string
	Kind       string
	State      string
}

func ListCapabilityEvidence(ctx context.Context, q Querier, f CapabilityEvidenceQuery, after string, limit int) (protocol.CapabilityEvidencePage, error) {
	p := protocol.CapabilityEvidencePage{Items: []protocol.ExecutorCapabilityEvidence{}}
	var maxSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM executor_capability_evidence`).Scan(&maxSeq); err != nil {
		return p, err
	}
	// Empty ledgers have no next cursor; one is a valid initial watermark.
	c, err := CapabilityCursor(f, after, max(maxSeq, 1))
	if err != nil {
		return p, err
	}
	p.AsOf = c.AsOf
	var last int64
	if c.After != "" {
		last, err = strconv.ParseInt(c.After, 10, 64)
		if err != nil {
			return p, protocol.Errorf(protocol.CodeInvalidRequest, "invalid evidence cursor")
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT sequence,evidence_json FROM executor_capability_evidence WHERE sequence>? AND sequence<=? AND (?='' OR host_id=?) AND (?='' OR agent_id=?) AND (?='' OR capability_id=?) AND (?='' OR json_extract(evidence_json,'$.kind')=?) AND (?='' OR json_extract(evidence_json,'$.state')=?) ORDER BY sequence LIMIT ?`, last, c.AsOf, f.Host, f.Host, f.Agent, f.Agent, f.Capability, f.Capability, f.Kind, f.Kind, f.State, f.State, limit+1)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int64
		var body string
		if err := rows.Scan(&sequence, &body); err != nil {
			return p, err
		}
		if len(p.Items) == limit {
			p.Next = c.Encode(strconv.FormatInt(last, 10))
			break
		}
		var e protocol.ExecutorCapabilityEvidence
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return p, err
		}
		p.Items = append(p.Items, e)
		last = sequence
	}
	return p, rows.Err()
}

// AgentCapabilityEvidence reads immutable facts without inferring currentness.
func AgentCapabilityEvidence(ctx context.Context, q Querier, host, agent string) ([]protocol.ExecutorCapabilityEvidence, error) {
	out := []protocol.ExecutorCapabilityEvidence{}
	rows, err := q.QueryContext(ctx, `SELECT evidence_json FROM executor_capability_evidence WHERE host_id=? AND agent_id=? ORDER BY sequence`, host, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		var e protocol.ExecutorCapabilityEvidence
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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

func (t *Tx) InsertRunCapabilityRevalidation(ctx context.Context, run string, r protocol.RunCapabilityRevalidation) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO run_capability_revalidations(run_id,request_id,revalidation_json) VALUES(?,?,?)`, run, r.RequestID, string(body))
	return err
}
func GetRunCapabilityRevalidation(ctx context.Context, q Querier, run string) (protocol.RunCapabilityRevalidation, error) {
	var r protocol.RunCapabilityRevalidation
	var body string
	err := q.QueryRowContext(ctx, `SELECT revalidation_json FROM run_capability_revalidations WHERE run_id=? ORDER BY sequence DESC LIMIT 1`, run).Scan(&body)
	if err != nil {
		return r, notFound(err, "run capability revalidation")
	}
	err = json.Unmarshal([]byte(body), &r)
	return r, err
}
