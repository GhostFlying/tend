package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type zeroReader struct{ remaining int64 }

func (r *zeroReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), r.remaining))
	clear(p[:n])
	r.remaining -= int64(n)
	return n, nil
}
func zeroHash(t *testing.T, size int64) string {
	t.Helper()
	h := sha256.New()
	if _, err := io.Copy(h, &zeroReader{remaining: size}); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func legacyFileLedger(t *testing.T, path string, size int64) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{write: db, read: db, now: testkit.NewClock().Now}
	if err := s.migrate(context.Background(), 30); err != nil {
		t.Fatal(err)
	}
	insertTask(t, s, "old-task")
	mustExec(t, s, `INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES('old-action','old-task','implement','old-effect','{}',1,'{}',1,'done','old-request',7,8)`)
	mustExec(t, s, `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at) VALUES('old-run','old-task','old-action',1,'implement','run','finished',7,8)`)
	mustExec(t, s, `INSERT INTO deliverable_files(file_id,task_id,run_id,name,size,sha256,content,created_at) VALUES('old-file','old-task','old-run','original.bin',?,?,zeroblob(?),19)`, size, zeroHash(t, size), size)
	mustExec(t, s, `INSERT INTO deliverable_files(file_id,task_id,run_id,name,size,sha256,content,created_at) VALUES('empty-file','old-task','old-run','empty.bin',0,?,X'',20)`, zeroHash(t, 0))
	mustExec(t, s, `INSERT INTO artifacts(artifact_id,task_id,run_id,kind,ref_json,digest,created_at) VALUES('old-artifact','old-task','old-run','result','{"files":[{"file_id":"old-file"}]}','original-digest',21)`)
	mustExec(t, s, `INSERT INTO conversation_messages(message_id,author,body,created_at) VALUES('old-message','owner','original text',23)`)
	mustExec(t, s, `INSERT INTO conversation_images(image_id,message_id,position,name,media_type,data) VALUES('old-image','old-message',0,'original.png','image/png',?)`, []byte("retained original image bytes"))
	return s
}

func TestLegacyFilesExportPreservesIDsHashesAndFileWindows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	const size = 23<<20 + 9
	old := legacyFileLedger(t, path, size)
	if err := old.write.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		s, err := Open(ctx, path, testkit.NewClock().Now)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Read(ctx, func(tx *ReadTx) error {
			f, err := GetDeliverableFile(ctx, tx.Q(), "old-file")
			if err != nil {
				return err
			}
			if f.Size != size || f.SHA256 != zeroHash(t, size) || f.TaskID != "old-task" || f.RunID != "old-run" || f.CreatedAt != 19 || f.Content != nil {
				t.Fatalf("file metadata changed: %+v", f)
			}
			var contentLength, externalized int
			if err := tx.Q().QueryRowContext(ctx, `SELECT length(content),externalized FROM deliverable_files WHERE file_id='old-file'`).Scan(&contentLength, &externalized); err != nil {
				return err
			}
			if contentLength != 0 || externalized != 1 {
				t.Fatalf("old BLOB retained after export: %d %d", contentLength, externalized)
			}
			window, err := ReadFinalizedDeliverableFile(ctx, tx.Q(), "old-file", size-7, 32, s.FileStore())
			if err != nil {
				return err
			}
			if len(window.Content) != 7 || !bytes.Equal(window.Content, make([]byte, 7)) {
				t.Fatal("wrong original byte window")
			}
			image, err := GetConversationImage(ctx, tx.Q(), "old-image")
			if err != nil {
				return err
			}
			if image.Size != int64(len("retained original image bytes")) || image.Data != nil {
				t.Fatalf("image metadata=%+v", image)
			}
			original, err := s.FileStore().Open(image.SHA256)
			if err != nil {
				return err
			}
			raw, err := io.ReadAll(original)
			original.Close()
			if err != nil {
				return err
			}
			if string(raw) != "retained original image bytes" {
				t.Fatal("image bytes changed")
			}
			attachments, err := ConversationAttachments(ctx, tx.Q(), "old-message")
			if err != nil {
				return err
			}
			if len(attachments) != 1 || attachments[0].FileID != "old-image" || attachments[0].SHA256 != image.SHA256 || attachments[0].Size != image.Size {
				t.Fatalf("original image association lost: %+v", attachments)
			}
			var body string
			if err := tx.Q().QueryRowContext(ctx, `SELECT body FROM conversation_messages WHERE message_id='old-message'`).Scan(&body); err != nil {
				return err
			}
			if body != "original text" {
				t.Fatal("message changed")
			}
			empty, err := s.FileStore().Open(zeroHash(t, 0))
			if err != nil {
				return err
			}
			stat, err := empty.Stat()
			empty.Close()
			if err != nil {
				return err
			}
			if stat.Size() != 0 {
				t.Fatal("empty original changed")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		t.Logf("open/reopen attempt %d preserved original content", attempt)
	}
}

func TestFault_LegacyBlobPublicationBeforeLedgerCommitResumes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	const size = 2<<20 + 3
	old := legacyFileLedger(t, path, size)
	// Model a crash after schema commit and file publication but before clearing
	// the BLOB. A fresh Open must verify/reuse the same object and finish once.
	if err := old.migrate(ctx, 31); err != nil {
		t.Fatal(err)
	}
	files, err := blobstore.New(path + ".files")
	if err != nil {
		t.Fatal(err)
	}
	published, err := files.Put(ctx, &zeroReader{remaining: size})
	if err != nil {
		t.Fatal(err)
	}
	if published.SHA256 != zeroHash(t, size) {
		t.Fatal("fixture hash mismatch")
	}
	if err := old.write.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var bytesLeft int
	if err := s.read.QueryRowContext(ctx, `SELECT length(content) FROM deliverable_files WHERE file_id='old-file'`).Scan(&bytesLeft); err != nil {
		t.Fatal(err)
	}
	if bytesLeft != 0 {
		t.Fatal("resumed export did not clear BLOB")
	}
	original, err := s.FileStore().Open(published.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if stat, err := original.Stat(); err != nil || stat.Size() != size {
		t.Fatalf("published original lost: %v %v", stat, err)
	}
}

func TestFault_LegacyBlobStorageFailureKeepsDatabaseBytes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	old := legacyFileLedger(t, path, 1024)
	if err := old.migrate(ctx, 31); err != nil {
		t.Fatal(err)
	}
	root := path + ".files"
	files, err := blobstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	old.files = files
	// A real filesystem failure after Open/New: the file root no longer exists.
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := old.exportLegacyBlobs(ctx); err == nil {
		t.Fatal("export unexpectedly succeeded without file storage")
	}
	var length int
	if err := old.read.QueryRowContext(ctx, `SELECT length(data) FROM conversation_images WHERE image_id='old-image'`).Scan(&length); err != nil {
		t.Fatal(err)
	}
	if length != len("retained original image bytes") {
		t.Fatal("failed export discarded original BLOB")
	}
	if err := old.write.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(ctx, path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	recovered.Close()
}

func TestLegacyBlobHashMismatchNeverDiscardsOriginal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	old := legacyFileLedger(t, path, 1024)
	mustExec(t, old, `UPDATE deliverable_files SET sha256=? WHERE file_id='old-file'`, zeroHash(t, 100))
	old.write.Close()
	s, err := Open(ctx, path, testkit.NewClock().Now)
	if err == nil {
		s.Close()
		t.Fatal("mismatched historical SHA accepted")
	}
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var length, externalized int
	if err := db.QueryRowContext(ctx, `SELECT length(content),externalized FROM deliverable_files WHERE file_id='old-file'`).Scan(&length, &externalized); err != nil {
		t.Fatal(err)
	}
	if length != 1024 || externalized != 0 {
		t.Fatalf("mismatched original changed: %d %d", length, externalized)
	}
}

func TestMessageAttachmentCanBeReusedAndMetadataNeverReadsOriginal(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	object, err := s.FileStore().Put(ctx, bytes.NewReader([]byte("shared original")))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		for _, id := range []string{"message-a", "message-b"} {
			if _, err := tx.InsertMessage(ctx, MessageRow{MessageID: id, Author: "owner"}); err != nil {
				return err
			}
		}
		if err := tx.InsertConversationAttachment(ctx, ConversationAttachmentRow{ConversationAttachment: protocol.ConversationAttachment{FileID: "attachment", Name: "shared.bin", MediaType: "application/octet-stream", Size: object.Size, SHA256: object.SHA256}}); err != nil {
			return err
		}
		for _, id := range []string{"message-a", "message-b"} {
			if err := tx.BindConversationAttachment(ctx, id, 0, "attachment"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path, err := s.FileStore().Path(object.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		for _, id := range []string{"message-a", "message-b"} {
			images, err := ConversationAttachments(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			if len(images) != 1 || images[0].Size != object.Size {
				t.Fatal("attachment metadata unavailable")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FileStore().Open(object.SHA256); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected visible original IO failure: %v", err)
	}
}
