package store

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestConversationBlockMigrationPreservesInterleavedTurns(t *testing.T) {
	ctx := t.Context()
	write, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "tend.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	old := &Store{write: write, read: write, now: testkit.NewClock().Now}
	if err := old.migrate(ctx, 38); err != nil {
		t.Fatal(err)
	}
	for i, author := range []string{"owner", "system", "owner"} {
		id := []string{"a", "event", "b"}[i]
		mustExec(t, old, `INSERT INTO conversation_messages(message_id,author,body,created_at) VALUES(?,?,?,?)`, id, author, id, (i+1)*10)
		mustExec(t, old, `INSERT INTO main_turns(request_id,message_id,thread_id,status,created_at,updated_at) VALUES(?,?,'thread','completed',?,?)`, id, id, (i+1)*10, (i+1)*10+1)
	}
	if err := old.migrate(ctx, 39); err != nil {
		t.Fatal(err)
	}
	rows, err := write.QueryContext(ctx, `SELECT message_id FROM conversation_blocks WHERE notice_only=0 ORDER BY position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "a" || ids[1] != "event" || ids[2] != "b" {
		t.Fatalf("processing order: %v", ids)
	}
}

func TestTranscriptRepliesUseIndexedSourcesAndPreservePagination(t *testing.T) {
	s, _ := openTest(t)
	seqs := map[string]int64{}
	if err := s.Write(t.Context(), func(tx *Tx) error {
		for _, m := range []MessageRow{
			{MessageID: "owner", Author: "owner", Body: "Question"},
			{MessageID: "other", Author: "owner", Body: "Other question"},
			{MessageID: "direct", Author: "main_agent", ReplyTo: "owner", Body: "Direct reply"},
			{MessageID: "shared", Author: "main_agent", ReplyTo: "other", Body: "Shared source reply"},
			{MessageID: "both", Author: "main_agent", ReplyTo: "owner", Body: "Both reply paths"},
			{MessageID: "foreign", Author: "main_agent", ReplyTo: "other", Body: "Unrelated reply"},
			{MessageID: "owner-reply", Author: "owner", ReplyTo: "owner", Body: "Owner follow-up"},
		} {
			seq, err := tx.InsertMessage(t.Context(), m)
			if err != nil {
				return err
			}
			seqs[m.MessageID] = seq
		}
		for _, reply := range []string{"shared", "both"} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO conversation_reply_sources(reply_id,source_id) VALUES(?,'owner')`, reply); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		rows, err := tx.Q().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+transcriptRepliesQuery, "owner", 0, "owner", 0, 20)
		if err != nil {
			return err
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				return err
			}
			plan = append(plan, detail)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		text := strings.Join(plan, "\n")
		if !strings.Contains(text, "conversation_replies") || !strings.Contains(text, "conversation_reply_sources_source") || strings.Contains(text, "SCAN m") {
			t.Fatalf("reply lookup lost its indexes: %s", text)
		}
		for _, test := range []struct {
			source string
			after  int64
			limit  int
			want   []string
		}{
			{"owner", 0, 20, []string{"direct", "shared", "both"}},
			{"owner", seqs["shared"], 20, []string{"both"}},
			{"owner", 0, 1, []string{"direct"}},
			{"other", 0, 20, []string{"shared", "foreign"}},
		} {
			replies, err := TranscriptReplies(t.Context(), tx.Q(), test.source, test.after, test.limit)
			if err != nil {
				return err
			}
			var ids []string
			for _, reply := range replies {
				ids = append(ids, reply.MessageID)
			}
			if !slices.Equal(ids, test.want) {
				t.Fatalf("reply source=%s after=%d limit=%d: %v want %v", test.source, test.after, test.limit, ids, test.want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
