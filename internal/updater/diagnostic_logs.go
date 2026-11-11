package updater

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

var errDiagnosticWriterActive = errors.New("updater: diagnostic writer is still active")

type diagnosticLogManifest struct {
	Format           int    `json:"format"`
	LaunchID         string `json:"launch_id"`
	ArtifactSHA256   string `json:"artifact_sha256"`
	Configuration    Config `json:"configuration"`
	ClosedAtMillis   int64  `json:"closed_at_ms,omitempty"`
	BodySHA256       string `json:"body_sha256,omitempty"`
	BodyBytes        int64  `json:"body_bytes,omitempty"`
	CompressedSHA256 string `json:"compressed_sha256,omitempty"`
}

func diagnosticLogPaths(cfg Config, id string) (string, string, error) {
	if cfg.StateDir == "" || !release.ValidID(id) {
		return "", "", errors.New("updater: invalid diagnostic launch identity")
	}
	root := filepath.Join(cfg.StateDir, "diagnostic-logs")
	return filepath.Join(root, id+".log"), filepath.Join(root, id+".json"), nil
}

// openDiagnosticWriter is shared by all four controlled launch paths. flock is
// inherited with stdout/stderr, so a surviving descendant also protects the log.
func openDiagnosticWriter(cfg Config, id, sha string) (*os.File, error) {
	if cfg.StateDir == "" {
		return os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	}
	path, manifest, err := diagnosticLogPaths(cfg, id)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("updater: unsafe diagnostic log")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) { _ = file.Close(); return nil, err }
	if err = lockDiagnosticWriter(file); err != nil {
		return fail(err)
	}
	metadata := diagnosticLogManifest{Format: 1, LaunchID: id, ArtifactSHA256: sha, Configuration: cfg}
	if err = publishLaunchFact(manifest, metadata); err != nil {
		return fail(err)
	}
	if err = syncDir(filepath.Dir(path)); err != nil {
		return fail(err)
	}
	// The compatibility viewer is a symlink only. A pre-existing regular legacy
	// log remains untouched and is reported separately as unknown/protected.
	if current, err := os.Lstat(cfg.LogPath); errors.Is(err, os.ErrNotExist) || err == nil && current.Mode()&os.ModeSymlink != 0 {
		if err == nil {
			target, readErr := os.Readlink(cfg.LogPath)
			if readErr != nil {
				return fail(readErr)
			}
			if filepath.Dir(target) != filepath.Dir(path) {
				return file, nil
			}
		}
		link := cfg.LogPath + ".current-" + id
		if err = os.Symlink(path, link); err != nil && !errors.Is(err, os.ErrExist) {
			return fail(err)
		}
		target, err := os.Readlink(link)
		if err != nil || target != path {
			return fail(errors.New("updater: current log link differs"))
		}
		if err = os.Rename(link, cfg.LogPath); err != nil {
			return fail(err)
		}
		if err = syncDir(filepath.Dir(cfg.LogPath)); err != nil {
			return fail(err)
		}
	}
	return file, nil
}

func readDiagnosticManifest(cfg Config, id string) (diagnosticLogManifest, error) {
	var metadata diagnosticLogManifest
	_, path, err := diagnosticLogPaths(cfg, id)
	if err != nil {
		return metadata, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return metadata, err
	}
	if err = json.Unmarshal(raw, &metadata); err != nil {
		return metadata, err
	}
	if metadata.Format != 1 || metadata.LaunchID != id || metadata.Configuration.InstanceID != cfg.InstanceID || metadata.Configuration.StateDir != cfg.StateDir || !release.ValidSHA(metadata.ArtifactSHA256) {
		return metadata, errors.New("updater: diagnostic manifest identity differs")
	}
	return metadata, nil
}

func (s *Supervisor) diagnosticLogPreview(ctx context.Context, policy protocol.BackupPolicy) ([]protocol.DiagnosticLog, error) {
	out := []protocol.DiagnosticLog{}
	root := filepath.Join(s.cfg.StateDir, "diagnostic-logs")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		record := protocol.DiagnosticLog{ID: id, Decision: "protected", Reasons: []string{}}
		metadata, readErr := readDiagnosticManifest(s.cfg, id)
		if readErr != nil {
			record.Reasons = append(record.Reasons, "diagnostic manifest is unknown")
			out = append(out, record)
			continue
		}
		rawPath, _, _ := diagnosticLogPaths(s.cfg, id)
		record.Path = rawPath
		record.ArtifactSHA256 = metadata.ArtifactSHA256
		if metadata.CompressedSHA256 != "" {
			record.Path = rawPath + ".gz"
			record.Compressed = true
			record.BodySHA256 = metadata.BodySHA256
			record.ClosedAtMillis = metadata.ClosedAtMillis
		}
		size, allocated, readErr := regularSize(record.Path)
		if readErr != nil {
			record.Reasons = append(record.Reasons, "diagnostic bytes are unavailable")
			out = append(out, record)
			continue
		}
		record.Bytes = size
		record.AllocatedBytes = allocated
		if reason := s.state.DiagnosticLogProtections[id]; reason != "" {
			record.Reasons = append(record.Reasons, "investigation protection: "+reason)
		}
		exit, readErr := LoadOwnedLaunchExit(metadata.Configuration, id, metadata.ArtifactSHA256)
		if readErr != nil || exit == nil {
			record.Reasons = append(record.Reasons, "original owned process exit is unconfirmed")
			out = append(out, record)
			continue
		}
		// A gzip file has no writer. If an interrupted compression left the original
		// inode, it must still be locked and reconciled before touching either copy.
		lockPath := rawPath
		if record.Compressed {
			if _, err := os.Lstat(rawPath); errors.Is(err, os.ErrNotExist) {
				lockPath = record.Path
			}
		}
		lock, readErr := lockClosedDiagnostic(lockPath)
		if readErr != nil {
			record.Reasons = append(record.Reasons, "a diagnostic writer is active or cannot be observed")
			out = append(out, record)
			continue
		}
		if !record.Compressed {
			info, err := lock.Stat()
			if err != nil {
				_ = lock.Close()
				return nil, err
			}
			record.ClosedAtMillis = info.ModTime().UnixMilli()
			hash, err := hashDiagnostic(ctx, lock)
			if err != nil {
				_ = lock.Close()
				return nil, err
			}
			record.BodySHA256 = hash
		}
		record.Closed = true
		_ = lock.Close()
		record.Decision = "keep"
		if len(record.Reasons) == 0 {
			age := s.opts.Now().UnixMilli() - record.ClosedAtMillis
			if age >= int64(policy.DiagnosticRetainDays)*int64(24*time.Hour/time.Millisecond) {
				record.Decision = "delete"
			} else if !record.Compressed && age >= int64(policy.DiagnosticCompressDays)*int64(24*time.Hour/time.Millisecond) {
				record.Decision = "compress"
			}
		} else {
			record.Decision = "protected"
		}
		out = append(out, record)
	}
	if info, err := os.Lstat(s.cfg.LogPath); err == nil && info.Mode().IsRegular() {
		out = append(out, protocol.DiagnosticLog{ID: "legacy", Path: s.cfg.LogPath, Bytes: info.Size(), AllocatedBytes: allocatedBytes(info), Decision: "protected", Reasons: []string{"legacy log has no inherited writer lock or complete owned generation identity"}})
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func hashDiagnostic(ctx context.Context, f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, &backupReader{ctx, f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Supervisor) ProtectDiagnosticLog(ctx context.Context, input protocol.DiagnosticLogProtection) (protocol.DiagnosticLogProtection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return input, err
	}
	if err := s.durabilityError(); err != nil {
		return input, err
	}
	if input.ExpectedGeneration != s.state.Generation {
		return input, errors.New("updater: diagnostic protection generation changed")
	}
	if _, err := readDiagnosticManifest(s.cfg, input.LogID); err != nil {
		return input, err
	}
	if s.state.DiagnosticLogProtections == nil {
		s.state.DiagnosticLogProtections = map[string]string{}
	}
	if input.Reason == "" {
		delete(s.state.DiagnosticLogProtections, input.LogID)
	} else {
		s.state.DiagnosticLogProtections[input.LogID] = input.Reason
	}
	return input, s.save()
}

func (s *Supervisor) pruneDiagnosticLog(ctx context.Context, record protocol.DiagnosticLog) (int64, int64, error) {
	path, manifest, err := diagnosticLogPaths(s.cfg, record.ID)
	if err != nil {
		return 0, 0, err
	}
	metadata, err := readDiagnosticManifest(s.cfg, record.ID)
	if errors.Is(err, os.ErrNotExist) && record.Decision == "delete" {
		for _, candidate := range []string{path, path + ".gz"} {
			if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
				return 0, 0, errors.New("updater: diagnostic metadata absent but bytes remain")
			}
		}
		return record.Bytes, record.AllocatedBytes, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if s.state.DiagnosticLogProtections[record.ID] != "" {
		return 0, 0, errors.New("updater: diagnostic investigation protection changed")
	}
	proof, err := LoadOwnedLaunchExit(metadata.Configuration, record.ID, metadata.ArtifactSHA256)
	if err != nil || proof == nil {
		return 0, 0, errors.New("updater: original diagnostic process exit is unconfirmed")
	}
	source := path
	if metadata.CompressedSHA256 != "" {
		source = path + ".gz"
	}
	lockPath := path
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		lockPath = source
	}
	lock, err := lockClosedDiagnostic(lockPath)
	if errors.Is(err, os.ErrNotExist) && record.Decision == "delete" {
		_ = os.Remove(manifest)
		return record.Bytes, record.AllocatedBytes, nil
	}
	if err != nil {
		return 0, 0, err
	}
	defer lock.Close()
	if record.Decision == "compress" {
		if metadata.CompressedSHA256 == "" {
			hash, err := hashDiagnostic(ctx, lock)
			if err != nil || hash != record.BodySHA256 {
				return 0, 0, errors.New("updater: original diagnostic bytes changed")
			}
			gzipHash, gzipBytes, err := compressDiagnostic(ctx, lock, path+".gz", record.BodySHA256, record.Bytes)
			if err != nil {
				return 0, 0, err
			}
			metadata.BodySHA256 = record.BodySHA256
			metadata.BodyBytes = record.Bytes
			metadata.ClosedAtMillis = record.ClosedAtMillis
			metadata.CompressedSHA256 = gzipHash
			if err = writeJSON(manifest, metadata); err != nil {
				return 0, 0, err
			}
			if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, 0, err
			}
			if err = syncDir(filepath.Dir(path)); err != nil {
				return 0, 0, err
			}
			_, allocated, err := regularSize(path + ".gz")
			return record.Bytes - gzipBytes, record.AllocatedBytes - allocated, err
		}
		// The original compression completed before its progress receipt. Verify
		// retained gzip and any surviving source, then finish the original removal.
		if metadata.BodySHA256 != record.BodySHA256 || metadata.BodyBytes != record.Bytes {
			return 0, 0, errors.New("updater: diagnostic compression identity differs")
		}
		if err = verifyDiagnosticGzip(ctx, path+".gz", metadata.BodySHA256, metadata.BodyBytes); err != nil {
			return 0, 0, err
		}
		if _, err = os.Lstat(path); err == nil {
			hash, err := hashDiagnostic(ctx, lock)
			if err != nil || hash != record.BodySHA256 {
				return 0, 0, errors.New("updater: remaining diagnostic source differs")
			}
			if err = os.Remove(path); err != nil {
				return 0, 0, err
			}
		}
		size, allocated, err := regularSize(path + ".gz")
		if err != nil {
			return 0, 0, err
		}
		return record.Bytes - size, record.AllocatedBytes - allocated, syncDir(filepath.Dir(path))
	}
	if record.Decision != "delete" {
		return 0, 0, errors.New("updater: diagnostic is not a deletion candidate")
	}
	if metadata.CompressedSHA256 != "" {
		if err = verifyDiagnosticGzip(ctx, source, metadata.BodySHA256, metadata.BodyBytes); err != nil {
			return 0, 0, err
		}
	} else {
		hash, err := hashDiagnostic(ctx, lock)
		if err != nil || hash != record.BodySHA256 {
			return 0, 0, errors.New("updater: diagnostic bytes changed")
		}
	}
	for _, target := range []string{path, path + ".gz", manifest} {
		if err = os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, 0, err
		}
	}
	return record.Bytes, record.AllocatedBytes, syncDir(filepath.Dir(path))
}
func compressDiagnostic(ctx context.Context, source *os.File, target, expected string, length int64) (string, int64, error) {
	if _, err := os.Lstat(target); err == nil {
		if err = verifyDiagnosticGzip(ctx, target, expected, length); err != nil {
			return "", 0, err
		}
		hash, err := hashFile(target)
		if err != nil {
			return "", 0, err
		}
		size, _, err := regularSize(target)
		return hash, size, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", 0, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".upload-log-")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	writer := gzip.NewWriter(temporary)
	_, copyErr := io.Copy(writer, &backupReader{ctx, source})
	if err = errors.Join(copyErr, writer.Close(), temporary.Sync()); err != nil {
		return "", 0, err
	}
	if err = verifyDiagnosticGzip(ctx, temporary.Name(), expected, length); err != nil {
		return "", 0, err
	}
	if err = os.Link(temporary.Name(), target); err != nil {
		return "", 0, err
	}
	if err = syncDir(filepath.Dir(target)); err != nil {
		return "", 0, err
	}
	hash, err := hashFile(target)
	if err != nil {
		return "", 0, err
	}
	size, _, err := regularSize(target)
	return hash, size, err
}
func verifyDiagnosticGzip(ctx context.Context, path, expected string, length int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer reader.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(&backupReader{ctx, reader}, length+1))
	if err != nil {
		return err
	}
	if n != length || hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("updater: compressed diagnostic content differs")
	}
	return nil
}
