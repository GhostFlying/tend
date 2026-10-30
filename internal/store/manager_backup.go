package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
)

// ManagerBackupReceipt names an immutable consistent ledger/content snapshot.
// The server-local directory is deliberately absent from public responses.
type ManagerBackupReceipt struct {
	ID           string `json:"id"`
	LedgerSHA256 string `json:"ledger_sha256"`
	Objects      int    `json:"objects"`
}

// BackupManagers snapshots SQLite with VACUUM INTO, then copies and verifies
// every content object referenced by that snapshot. Concurrent new metadata is
// excluded by SQLite's snapshot; immutable source objects are never rewritten.
// The receipt is published only after both ledger and bytes are durable.
func (s *Store) BackupManagers(ctx context.Context, id string) (ManagerBackupReceipt, error) {
	var receipt ManagerBackupReceipt
	if !protocol.ValidID(protocol.PrefixRequest, id) {
		return receipt, errors.New("backup requires a request identity")
	}
	root := s.path + ".manager-backups"
	if err := os.MkdirAll(root, 0700); err != nil {
		return receipt, err
	}
	final := filepath.Join(root, id)
	if raw, err := os.ReadFile(filepath.Join(final, "receipt.json")); err == nil {
		if err = json.Unmarshal(raw, &receipt); err != nil {
			return receipt, err
		}
		if receipt.ID != id {
			return receipt, errors.New("backup receipt identity differs")
		}
		return receipt, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return receipt, err
	}
	tmp, err := os.MkdirTemp(root, ".snapshot-")
	if err != nil {
		return receipt, err
	}
	defer os.RemoveAll(tmp)
	ledger := filepath.Join(tmp, "ledger.db")
	if _, err = s.write.ExecContext(ctx, `VACUUM INTO ?`, ledger); err != nil {
		return receipt, fmt.Errorf("snapshot ledger: %w", err)
	}
	snapshot, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: ledger}).String()+"?mode=ro&immutable=1")
	if err != nil {
		return receipt, err
	}
	defer snapshot.Close()
	rows, err := snapshot.QueryContext(ctx, `SELECT sha256 FROM conversation_attachments UNION SELECT sha256 FROM deliverable_files UNION SELECT sha256 FROM conversation_images WHERE sha256<>'' ORDER BY 1`)
	if err != nil {
		return receipt, err
	}
	hashes := []string{}
	for rows.Next() {
		var hash string
		if err = rows.Scan(&hash); err != nil {
			rows.Close()
			return receipt, err
		}
		hashes = append(hashes, hash)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return receipt, err
	}
	files, err := blobstore.New(ledger + ".files")
	if err != nil {
		return receipt, err
	}
	for _, hash := range hashes {
		source, err := s.files.Open(hash)
		if err != nil {
			return receipt, err
		}
		object, copyErr := files.Put(ctx, source)
		err = errors.Join(copyErr, source.Close())
		if err != nil {
			return receipt, err
		}
		if object.SHA256 != hash {
			return receipt, errors.New("snapshot object differs from ledger")
		}
	}
	f, err := os.OpenFile(ledger, os.O_RDWR, 0600)
	if err != nil {
		return receipt, err
	}
	digest := sha256.New()
	_, err = io.Copy(digest, f)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return receipt, err
	}
	receipt = ManagerBackupReceipt{ID: id, LedgerSHA256: hex.EncodeToString(digest.Sum(nil)), Objects: len(hashes)}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return receipt, err
	}
	marker, err := os.OpenFile(filepath.Join(tmp, "receipt.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return receipt, err
	}
	_, err = marker.Write(raw)
	if err == nil {
		err = marker.Sync()
	}
	err = errors.Join(err, marker.Close())
	if err != nil {
		return receipt, err
	}
	if err = snapshot.Close(); err != nil {
		return receipt, err
	}
	if err = os.Rename(tmp, final); err != nil {
		// A concurrent retry may already have published the original identity.
		if raw, readErr := os.ReadFile(filepath.Join(final, "receipt.json")); readErr == nil {
			err = json.Unmarshal(raw, &receipt)
		}
		if err != nil {
			return receipt, err
		}
	}
	dir, err := os.Open(root)
	if err != nil {
		return receipt, err
	}
	return receipt, errors.Join(dir.Sync(), dir.Close())
}
