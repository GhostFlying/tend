package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Packet states.
const (
	PacketBuilt    = "built"
	PacketInjected = "injected"
	PacketFailed   = "failed"
	PacketUnknown  = "unknown"
)

// Packet is a context packet for a thread.
type Packet struct {
	PacketID  string
	ThreadKey string
	Epoch     int64
	Kind      string
	FromSeq   int64
	ToSeq     int64
	Body      string
	Status    string
}

// Baseline is what a thread's model has seen of the ledger.
type Baseline struct {
	ThreadKey    string
	Epoch        int64
	Watermark    int64
	LastPacketID string
	Valid        bool
}

// GetBaseline returns a thread's baseline, or ErrNotFound.
func GetBaseline(ctx context.Context, q Querier, threadKey string) (Baseline, error) {
	b := Baseline{ThreadKey: threadKey}
	var last sql.NullString
	err := q.QueryRowContext(ctx, `SELECT epoch, watermark_seq, last_packet_id, valid FROM thread_baselines WHERE thread_key = ?`, threadKey).
		Scan(&b.Epoch, &b.Watermark, &last, &b.Valid)
	if err != nil {
		return Baseline{}, notFound(err, "baseline of "+threadKey)
	}
	b.LastPacketID = last.String
	return b, nil
}

// PutBaseline creates or replaces a thread's baseline.
func (t *Tx) PutBaseline(ctx context.Context, b Baseline, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO thread_baselines (thread_key, epoch, watermark_seq, last_packet_id, valid, updated_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (thread_key) DO UPDATE SET epoch = excluded.epoch,
		watermark_seq = excluded.watermark_seq, last_packet_id = excluded.last_packet_id, valid = excluded.valid,
		updated_at = excluded.updated_at`, b.ThreadKey, b.Epoch, b.Watermark, nullString(b.LastPacketID), b.Valid, now)
	if err != nil {
		return fmt.Errorf("store: put baseline: %w", err)
	}
	return nil
}

// InsertPacket stores a packet in the built state.
func (t *Tx) InsertPacket(ctx context.Context, p Packet, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO context_packets (packet_id, thread_key, epoch, kind, from_seq, to_seq, body, status,
		created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'built', ?, ?)`,
		p.PacketID, p.ThreadKey, p.Epoch, p.Kind, p.FromSeq, p.ToSeq, p.Body, now, now)
	if err != nil {
		return fmt.Errorf("store: insert packet: %w", err)
	}
	return nil
}

// GetPacket returns a packet, or ErrNotFound.
func GetPacket(ctx context.Context, q Querier, packetID string) (Packet, error) {
	p := Packet{PacketID: packetID}
	err := q.QueryRowContext(ctx, `SELECT thread_key, epoch, kind, from_seq, to_seq, body, status FROM context_packets WHERE packet_id = ?`, packetID).
		Scan(&p.ThreadKey, &p.Epoch, &p.Kind, &p.FromSeq, &p.ToSeq, &p.Body, &p.Status)
	if err != nil {
		return Packet{}, notFound(err, "packet "+packetID)
	}
	return p, nil
}

// SetPacketStatus moves a packet out of a state; it returns ErrStale when
// the packet is no longer in from.
func (t *Tx) SetPacketStatus(ctx context.Context, packetID, from, to string, now int64) error {
	res, err := t.Exec(ctx, `UPDATE context_packets SET status = ?, updated_at = ? WHERE packet_id = ? AND status = ?`, to, now, packetID, from)
	if err != nil {
		return fmt.Errorf("store: set packet status: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ErrStale
	}
	return nil
}

// SettleBuiltPackets marks every packet of a thread still waiting for
// confirmation as unknown.
func (t *Tx) SettleBuiltPackets(ctx context.Context, threadKey string, now int64) error {
	_, err := t.Exec(ctx, `UPDATE context_packets SET status = 'unknown', updated_at = ? WHERE thread_key = ? AND status = 'built'`, now, threadKey)
	if err != nil {
		return fmt.Errorf("store: settle packets: %w", err)
	}
	return nil
}

// LatestEventSeq returns the sequence number of the newest event, or zero.
func LatestEventSeq(ctx context.Context, q Querier) (int64, error) {
	var seq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq), 0) FROM events`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("store: latest event: %w", err)
	}
	return seq, nil
}

// EventView is an event as read back.
type EventView struct {
	Seq        int64
	ObjectType string
	ObjectID   string
	Kind       string
	BodyJSON   string
	// RequestID is the request whose execution recorded the event, if any.
	RequestID string
	// ActorJSON is that request's principal, if any.
	ActorJSON string
	CreatedAt int64
}

// ListEventsAfter returns events after seq up to and including through,
// whose kind is in kinds, in order.
func ListEventsAfter(ctx context.Context, q Querier, seq, through int64, kinds []string) ([]EventView, error) {
	rows, err := q.QueryContext(ctx, `SELECT event_seq, object_type, object_id, kind, body_json, request_id, actor_json, created_at FROM events
		WHERE event_seq > ? AND event_seq <= ? AND kind IN (SELECT value FROM json_each(?)) ORDER BY event_seq`,
		seq, through, jsonList(kinds))
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()
	var out []EventView
	for rows.Next() {
		var e EventView
		var request, actor sql.NullString
		if err := rows.Scan(&e.Seq, &e.ObjectType, &e.ObjectID, &e.Kind, &e.BodyJSON, &request, &actor, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		e.RequestID, e.ActorJSON = request.String, actor.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// MessageView is a conversation message as read back.
type MessageView struct {
	Seq       int64
	MessageID string
	Author    string
	Body      string
	ReplyTo   string
	CreatedAt int64
	// LastSeenSeq is the last message the sender's client showed when the
	// message was written; zero when unknown.
	LastSeenSeq int64
	// Bindings are the objects the message was tied to, as type:id.
	Bindings []string
}

// RecentMessages returns the last n conversation messages in order, up to
// and including the message with seq through when through is positive.
func RecentMessages(ctx context.Context, q Querier, n int, through int64) ([]MessageView, error) {
	rows, err := q.QueryContext(ctx, `SELECT seq, message_id, author, body, COALESCE(reply_to, ''), created_at FROM (
		SELECT * FROM conversation_messages WHERE ? <= 0 OR seq <= ? ORDER BY seq DESC LIMIT ?) ORDER BY seq`, through, through, n)
	if err != nil {
		return nil, fmt.Errorf("store: recent messages: %w", err)
	}
	var out []MessageView
	for rows.Next() {
		var m MessageView
		if err := rows.Scan(&m.Seq, &m.MessageID, &m.Author, &m.Body, &m.ReplyTo, &m.CreatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		b, err := q.QueryContext(ctx, `SELECT object_type || ':' || object_id FROM conversation_bindings WHERE message_id = ? ORDER BY object_type, object_id`, out[i].MessageID)
		if err != nil {
			return nil, fmt.Errorf("store: bindings: %w", err)
		}
		for b.Next() {
			var s string
			if err := b.Scan(&s); err != nil {
				b.Close()
				return nil, err
			}
			out[i].Bindings = append(out[i].Bindings, s)
		}
		if err := b.Err(); err != nil {
			b.Close()
			return nil, err
		}
		b.Close()
	}
	return out, nil
}

// GetMessageSeq returns the sequence number of a conversation message, or
// ErrNotFound.
func GetMessageSeq(ctx context.Context, q Querier, messageID string) (int64, error) {
	var seq int64
	if err := q.QueryRowContext(ctx, `SELECT seq FROM conversation_messages WHERE message_id = ?`, messageID).Scan(&seq); err != nil {
		return 0, notFound(err, "message "+messageID)
	}
	return seq, nil
}

// LatestPacket returns a thread's newest packet, or ErrNotFound.
func LatestPacket(ctx context.Context, q Querier, threadKey string) (Packet, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT packet_id FROM context_packets WHERE thread_key = ? ORDER BY created_at DESC, packet_id DESC LIMIT 1`, threadKey).Scan(&id)
	if err != nil {
		return Packet{}, notFound(err, "packet of "+threadKey)
	}
	return GetPacket(ctx, q, id)
}
