package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetExtensionState(ctx context.Context, q Querier) (protocol.ExtensionState, error) {
	s := protocol.ExtensionState{Catalog: protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{}, Categories: []protocol.ExtensionBinding{}, Agents: []protocol.ExtensionBinding{}}}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT state_json FROM extension_state ORDER BY revision DESC LIMIT 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal([]byte(raw), &s)
	return s, err
}

func (t *Tx) InsertExtensionState(ctx context.Context, s protocol.ExtensionState, request string) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO extension_state(revision,state_json,request_id) VALUES(?,?,?)`, s.Revision, string(raw), request)
	return err
}

func ExtensionContentExists(ctx context.Context, q Querier, r protocol.ExtensionResource) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM extension_content WHERE sha256=? AND kind=? AND size=?)`, r.ContentSHA256, r.Kind, r.Size).Scan(&exists)
	return exists, err
}

func (t *Tx) InsertExtensionContent(ctx context.Context, r protocol.ExtensionResource, request string) error {
	_, err := t.Exec(ctx, `INSERT INTO extension_content(sha256,kind,size,request_id) VALUES(?,?,?,?) ON CONFLICT(sha256,kind) DO NOTHING`, r.ContentSHA256, r.Kind, r.Size, request)
	return err
}

func GetExtensionRelease(ctx context.Context, q Querier, id string) (protocol.ExtensionRelease, protocol.ExtensionReleaseRef, error) {
	var release protocol.ExtensionRelease
	var ref protocol.ExtensionReleaseRef
	var manifest, reference string
	err := q.QueryRowContext(ctx, `SELECT manifest_json,reference_json FROM extension_releases WHERE release_id=?`, id).Scan(&manifest, &reference)
	if err != nil {
		return release, ref, notFound(err, "extension release")
	}
	if err = json.Unmarshal([]byte(manifest), &release); err != nil {
		return release, ref, err
	}
	err = json.Unmarshal([]byte(reference), &ref)
	return release, ref, err
}

func (t *Tx) InsertExtensionRelease(ctx context.Context, r protocol.ExtensionRelease, ref protocol.ExtensionReleaseRef, request string) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	reference, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO extension_releases(release_id,manifest_json,reference_json,request_id) VALUES(?,?,?,?) ON CONFLICT(release_id) DO NOTHING`, r.ID, string(raw), string(reference), request)
	return err
}

func ListExtensionReleases(ctx context.Context, q Querier, after string, limit int) (protocol.ExtensionReleasePage, error) {
	p := protocol.ExtensionReleasePage{Items: []protocol.ExtensionReleaseRef{}}
	rows, err := q.QueryContext(ctx, `SELECT reference_json FROM extension_releases WHERE release_id>? ORDER BY release_id LIMIT ?`, after, limit+1)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return p, err
		}
		if len(p.Items) == limit {
			p.More = true
			break
		}
		var ref protocol.ExtensionReleaseRef
		if err = json.Unmarshal([]byte(raw), &ref); err != nil {
			return p, err
		}
		p.Items = append(p.Items, ref)
	}
	if p.More {
		p.Next = p.Items[len(p.Items)-1].ID
	}
	return p, rows.Err()
}

// PublishedExtensionContent excludes staged, unpublished owner uploads.
func PublishedExtensionContent(ctx context.Context, q Querier, release, hash string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM extension_releases r,json_each(r.manifest_json,'$.catalog.resources') p WHERE r.release_id=? AND json_extract(p.value,'$.content_sha256')=?)`, release, hash).Scan(&exists)
	return exists, err
}

func ExtensionConversationCategory(ctx context.Context, q Querier, conversation string) (string, int64, error) {
	var category string
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT category_id,revision FROM extension_conversation_categories WHERE conversation_id=?`, ConversationID(conversation)).Scan(&category, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	return category, revision, err
}
func (t *Tx) PutExtensionConversationCategory(ctx context.Context, conversation, category string, revision int64) error {
	_, err := t.Exec(ctx, `INSERT INTO extension_conversation_categories(conversation_id,category_id,revision) VALUES(?,?,?) ON CONFLICT(conversation_id) DO UPDATE SET category_id=excluded.category_id,revision=excluded.revision`, ConversationID(conversation), category, revision)
	return err
}

func ExtensionResourceVersion(ctx context.Context, q Querier, kind, id, version string) (protocol.ExtensionResource, error) {
	var r protocol.ExtensionResource
	var raw string
	err := q.QueryRowContext(ctx, `SELECT resource_json FROM extension_resource_versions WHERE kind=? AND resource_id=? AND version=?`, kind, id, version).Scan(&raw)
	if err != nil {
		return r, notFound(err, "extension resource version")
	}
	err = json.Unmarshal([]byte(raw), &r)
	return r, err
}
func (t *Tx) InsertExtensionResourceVersion(ctx context.Context, r protocol.ExtensionResource) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO extension_resource_versions(kind,resource_id,version,resource_json) VALUES(?,?,?,?) ON CONFLICT(kind,resource_id,version) DO NOTHING`, r.Kind, r.ID, r.Version, string(raw))
	return err
}
func ExtensionBindingRevision(ctx context.Context, q Querier, id string) (int64, error) {
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM extension_binding_versions WHERE binding_id=?`, id).Scan(&revision)
	return revision, err
}
func (t *Tx) InsertExtensionBindingVersion(ctx context.Context, id string, b protocol.ExtensionBinding) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO extension_binding_versions(binding_id,revision,binding_json) VALUES(?,?,?)`, id, b.Revision, string(raw))
	return err
}
