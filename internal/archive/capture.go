package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Capture has a stopped-writer precondition. It does not stop any live process.
// A failed destination has no manifest and is never a readable completed archive.
func Capture(ctx context.Context, options CaptureOptions) (Manifest, error) {
	var manifest Manifest
	if !options.WritersStopped || options.CapturedAt.IsZero() {
		return manifest, errors.New("archive: stopped writers and capture time are required")
	}
	source, err := filepath.Abs(options.Database)
	if err != nil {
		return manifest, err
	}
	destination, err := filepath.Abs(options.Destination)
	if err != nil {
		return manifest, err
	}
	if _, err = regularFile(source); err != nil {
		return manifest, err
	}
	if containsPath(destination, source) {
		return manifest, errors.New("archive: destination overlaps source")
	}
	if options.BlobRoot == "" {
		options.BlobRoot = source + ".files"
	}
	blobRoot, err := filepath.Abs(options.BlobRoot)
	if err != nil {
		return manifest, err
	}
	if containsPath(blobRoot, destination) || containsPath(destination, blobRoot) {
		return manifest, errors.New("archive: destination overlaps original files")
	}
	for _, resource := range options.Resources {
		if resource.Path != "" {
			path, err := filepath.Abs(resource.Path)
			if err != nil {
				return manifest, err
			}
			if containsPath(path, destination) || containsPath(destination, path) {
				return manifest, errors.New("archive: destination overlaps selected resource")
			}
		}
	}
	if err = os.Mkdir(destination, 0o700); err != nil {
		return manifest, err
	}
	raw := filepath.Join(destination, "raw", "ledger.db")
	if err = copySQLiteFiles(ctx, source, raw); err != nil {
		return manifest, err
	}
	working := filepath.Join(destination, ".working", "ledger.db")
	if err = copySQLiteFiles(ctx, raw, working); err != nil {
		return manifest, err
	}
	snapshot := filepath.Join(destination, "snapshot.sqlite")
	if err = snapshotFromCopy(ctx, working, snapshot); err != nil {
		return manifest, err
	}
	db, err := openSnapshot(ctx, snapshot)
	if err != nil {
		return manifest, err
	}
	defer db.Close()
	identity, migrations, err := inspectLedger(ctx, db)
	if err != nil {
		return manifest, err
	}
	manifest = Manifest{FormatVersion: FormatVersion, CreatedAt: options.CapturedAt, DeploymentID: identity, Schema: len(migrations), Migrations: migrations, SourceBuild: options.SourceBuild, Snapshot: "snapshot.sqlite", Originals: []Original{}, Resources: []Resource{}}
	// Retain completed and unreferenced local original material, without using
	// blobstore.New or changing source layout. Missing referenced objects fail below.
	if info, statErr := os.Stat(blobRoot); statErr == nil && info.IsDir() {
		if err = copyTree(ctx, blobRoot, filepath.Join(destination, "raw", "blobstore")); err != nil {
			return manifest, err
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return manifest, statErr
	}
	if err = exportOriginals(ctx, db, destination, &manifest); err != nil {
		return manifest, err
	}
	if err = captureResources(ctx, db, destination, options.Resources, &manifest); err != nil {
		return manifest, err
	}
	if err = os.RemoveAll(filepath.Join(destination, ".working")); err != nil {
		return manifest, err
	}
	err = filepath.WalkDir(destination, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(destination, path)
		if err != nil {
			return err
		}
		kind := "resource"
		if relative == "snapshot.sqlite" {
			kind = "ledger_snapshot"
		} else if strings.HasPrefix(relative, "raw") {
			kind = "raw_evidence"
		} else if strings.HasPrefix(relative, "originals") {
			kind = "original"
		}
		file, err := payload(ctx, destination, filepath.ToSlash(relative), kind)
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, file)
		return nil
	})
	if err != nil {
		return manifest, err
	}
	slices.SortFunc(manifest.Files, func(a, b Payload) int { return strings.Compare(a.Path, b.Path) })
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	if err = syncPayloadDirectories(destination); err != nil {
		return manifest, err
	}
	if err = writeBytes(filepath.Join(destination, "manifest.json"), append(encoded, '\n')); err != nil {
		return manifest, err
	}
	if err = syncDirectory(destination); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func copySQLiteFiles(ctx context.Context, source, destination string) error {
	if err := copyFile(ctx, source, destination); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_, err := os.Lstat(source + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err = copyFile(ctx, source+suffix, destination+suffix); err != nil {
			return err
		}
	}
	return nil
}
func copyTree(ctx context.Context, source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(ctx, path, target)
	})
}

func exportOriginals(ctx context.Context, db *sql.DB, root string, manifest *Manifest) error {
	queries := []struct{ kind, sql string }{
		{"attachment", `SELECT file_id,name,media_type,size,sha256,NULL FROM conversation_attachments ORDER BY file_id`},
		{"deliverable", `SELECT file_id,name,'',size,sha256,CASE WHEN externalized=0 THEN content ELSE NULL END FROM deliverable_files ORDER BY file_id`},
		{"image", `SELECT image_id,name,media_type,size,sha256,CASE WHEN length(data)>0 OR sha256='' THEN data ELSE NULL END FROM conversation_images ORDER BY image_id`},
	}
	for _, query := range queries {
		rows, err := db.QueryContext(ctx, query.sql)
		if err != nil {
			return err
		}
		for rows.Next() {
			original := Original{Kind: query.kind}
			var content any
			if err = rows.Scan(&original.ID, &original.Name, &original.MediaType, &original.Size, &original.SHA256, &content); err != nil {
				break
			}
			inline, isInline := content.([]byte)
			if content != nil && !isInline {
				err = errors.New("archive: invalid inline original content")
				break
			}
			if isInline {
				digest := sha256.Sum256(inline)
				hash := hex.EncodeToString(digest[:])
				if original.SHA256 != "" && (original.SHA256 != hash || original.Size != int64(len(inline))) {
					err = fmt.Errorf("archive: original metadata mismatch: %s/%s", query.kind, original.ID)
					break
				}
				original.Size = int64(len(inline))
				original.SHA256 = hash
			}
			if len(original.SHA256) != 64 {
				err = fmt.Errorf("archive: original has no usable content hash: %s/%s", query.kind, original.ID)
				break
			}
			if _, hashErr := hex.DecodeString(original.SHA256); hashErr != nil {
				err = hashErr
				break
			}
			original.Path = "originals/" + original.SHA256
			target := filepath.Join(root, filepath.FromSlash(original.Path))
			if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
				if isInline {
					err = writeBytes(target, inline)
				} else {
					err = copyFile(ctx, filepath.Join(root, "raw", "blobstore", original.SHA256[:2], original.SHA256), target)
				}
				if err != nil {
					break
				}
			} else if statErr != nil {
				err = statErr
				break
			}
			size, hash, hashErr := hashFile(ctx, target)
			if hashErr != nil {
				err = hashErr
				break
			}
			if size != original.Size || hash != original.SHA256 {
				err = fmt.Errorf("archive: original content mismatch: %s/%s", query.kind, original.ID)
				break
			}
			manifest.Originals = append(manifest.Originals, original)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
	}
	return nil
}

func captureResources(ctx context.Context, db *sql.DB, root string, sources []ResourceSource, manifest *Manifest) error {
	seen := map[string]bool{}
	providedJournals := map[string]bool{}
	providedNative := map[string]bool{}
	providedThreads := map[string]bool{}
	for _, source := range sources {
		if source.ID == "" || source.Scope == "" || seen[source.ID] {
			return errors.New("archive: resources require unique IDs and explicit owner-selected scope")
		}
		seen[source.ID] = true
		if !slices.Contains([]string{"runner_journal", "native_history", "baseline", "report"}, source.Kind) {
			return errors.New("archive: unsupported resource kind")
		}
		if !slices.Contains([]string{"collected", "partial", "missing", "not_collected"}, source.Status) {
			return errors.New("archive: invalid resource coverage")
		}
		resource := Resource{ID: source.ID, Kind: source.Kind, HostID: source.HostID, RunID: source.RunID, ThreadID: source.ThreadID, ParentThreadID: source.ParentThreadID, Scope: source.Scope, Status: source.Status, Reason: source.Reason}
		if source.Path == "" {
			if source.Status == "collected" || source.Status == "partial" || source.Reason == "" {
				return errors.New("archive: absent resource requires explicit missing/not_collected reason")
			}
		} else {
			if source.Status != "collected" && source.Status != "partial" {
				return errors.New("archive: provided resource requires collected/partial coverage")
			}
			digest := sha256.Sum256([]byte(source.ID))
			relative := "resources/" + hex.EncodeToString(digest[:])
			target := filepath.Join(root, filepath.FromSlash(relative))
			info, err := os.Stat(source.Path)
			if err != nil {
				return err
			}
			if source.Kind == "runner_journal" {
				if !info.Mode().IsRegular() {
					return errors.New("archive: runner journal must be a stopped SQLite file")
				}
				raw := filepath.Join(root, "raw", "resources", hex.EncodeToString(digest[:]), "journal.db")
				if err = copySQLiteFiles(ctx, source.Path, raw); err != nil {
					return err
				}
				work := filepath.Join(root, ".working", hex.EncodeToString(digest[:]), "journal.db")
				if err = copySQLiteFiles(ctx, raw, work); err != nil {
					return err
				}
				if err = snapshotFromCopy(ctx, work, target); err != nil {
					return err
				}
				j, err := openSnapshot(ctx, target)
				if err != nil {
					return err
				}
				var version int
				err = j.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
				err = errors.Join(err, j.Close())
				if err != nil {
					return err
				}
				resource.JournalSchema = version
				if version < 10 || version > 12 {
					return fmt.Errorf("archive: unsupported runner journal schema %d", version)
				}
			} else if info.IsDir() {
				if err = copyTree(ctx, source.Path, target); err != nil {
					return err
				}
			} else {
				if err = copyFile(ctx, source.Path, target); err != nil {
					return err
				}
			}
			err = filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				relative, err := filepath.Rel(root, path)
				if err == nil {
					resource.Files = append(resource.Files, filepath.ToSlash(relative))
				}
				return err
			})
			if err != nil {
				return err
			}
		}
		if source.Kind == "runner_journal" && source.HostID != "" {
			providedJournals[source.HostID] = true
		}
		if source.Kind == "native_history" && source.RunID != "" {
			providedNative[source.RunID] = true
		}
		if source.Kind == "native_history" && source.ThreadID != "" {
			providedThreads[source.ThreadID] = true
		}
		manifest.Resources = append(manifest.Resources, resource)
	}
	// The raw snapshot preserves server history verbatim; its coverage is not a
	// claim that the server ever received complete native transcript text.
	manifest.Resources = append(manifest.Resources, Resource{ID: "ledger-recorded-history", Kind: "ledger_history", Scope: "All stored server rows, including original JSON and recorded truncation metadata", Status: "collected", Files: []string{manifest.Snapshot}, Reason: "Server logs may already be truncated; no native history is synthesized"})
	for _, kind := range []string{"host", "run", "thread"} {
		query := "SELECT host_id FROM hosts ORDER BY host_id"
		if kind == "run" {
			query = "SELECT run_id FROM runs ORDER BY run_id"
		}
		if kind == "thread" {
			query = "SELECT thread_id FROM agent_thread_history ORDER BY thread_id"
		}
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			provided := providedJournals[id]
			resourceKind := "runner_journal"
			if kind == "run" {
				provided = providedNative[id]
				resourceKind = "native_history"
			}
			if kind == "thread" {
				provided = providedThreads[id]
				resourceKind = "native_history"
			}
			if provided {
				continue
			}
			resource := Resource{ID: "uncollected-" + kind + "-" + id, Kind: resourceKind, Scope: "No owner-selected local resource supplied", Status: "not_collected", Reason: "Ledger projection does not prove complete native/journal coverage"}
			if kind == "host" {
				resource.HostID = id
			} else if kind == "thread" {
				resource.ThreadID = id
			} else {
				resource.RunID = id
			}
			manifest.Resources = append(manifest.Resources, resource)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
	}
	return nil
}
