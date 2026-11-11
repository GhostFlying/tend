package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
)

const backupMetadataName = "checkpoint.json"

type backupMetadata struct {
	Format        int                       `json:"format"`
	InstanceID    string                    `json:"instance_id"`
	Configuration Config                    `json:"configuration"`
	Source        Version                   `json:"source"`
	Checkpoint    protocol.BackupCheckpoint `json:"checkpoint"`
}

func DefaultBackupPolicy() protocol.BackupPolicy {
	return protocol.BackupPolicy{MinimumVerified: 2, Daily: 7, Weekly: 4, MigrationDays: 7, IncompatibleDays: 30, SuccessfulDrillHours: 24, FailedDrillDays: 7, DiagnosticCompressDays: 7, DiagnosticRetainDays: 30}
}
func normalizeBackupPolicy(p protocol.BackupPolicy) (protocol.BackupPolicy, error) {
	if p == (protocol.BackupPolicy{}) {
		return DefaultBackupPolicy(), nil
	}
	if p.DiagnosticCompressDays == 0 {
		p.DiagnosticCompressDays = 7
	}
	if p.DiagnosticRetainDays == 0 {
		p.DiagnosticRetainDays = 30
	}
	if p.DiagnosticCompressDays < 7 || p.DiagnosticRetainDays < 30 || p.DiagnosticRetainDays <= p.DiagnosticCompressDays {
		return p, errors.New("updater: invalid diagnostic log policy")
	}
	if p.MinimumVerified < 2 || p.Daily < 0 || p.Weekly < 0 || p.MigrationDays < 7 || p.IncompatibleDays < 30 || p.SuccessfulDrillHours < 24 || p.FailedDrillDays < 7 {
		return p, errors.New("updater: invalid backup retention policy")
	}
	return p, nil
}
func backupID(cfg Config, dir string) (string, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", errors.New("updater: invalid checkpoint path")
	}
	for _, root := range []string{"backups", "maintenance-backups"} {
		base := filepath.Join(cfg.StateDir, root)
		if filepath.Dir(dir) == base && filepath.Base(dir) != "." {
			return root + "/" + filepath.Base(dir), nil
		}
	}
	return "", errors.New("updater: checkpoint escapes configured backup roots")
}
func backupDir(cfg Config, id string) (string, error) {
	dir := filepath.Join(cfg.StateDir, filepath.FromSlash(id))
	actual, err := backupID(cfg, dir)
	if err != nil || actual != id {
		return "", errors.New("updater: invalid checkpoint identity")
	}
	return dir, nil
}
func writeBackupMetadata(cfg Config, op *Operation) error {
	// Tests and dedicated recovery readers may use an explicit temporary snapshot
	// without a supervisor. Such snapshots remain legacy manifests, not governed
	// checkpoints eligible for deletion.
	if cfg.StateDir == "" {
		return nil
	}
	id, err := backupID(cfg, op.BackupDir)
	if err != nil {
		return err
	}
	path := filepath.Join(op.BackupDir, backupMetadataName)
	if _, err = os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	manifest, err := hashFile(filepath.Join(op.BackupDir, "files.json"))
	if err != nil {
		return err
	}
	created := op.StartedAt.UnixMilli()
	if op.StartedAt.IsZero() {
		info, err := os.Stat(filepath.Join(op.BackupDir, "files.json"))
		if err != nil {
			return err
		}
		created = info.ModTime().UnixMilli()
	}
	record := protocol.BackupCheckpoint{ID: id, Directory: op.BackupDir, CreatedAtMillis: created, ManifestSHA256: manifest, Class: "checkpoint", Unknown: true}
	return writeJSON(path, backupMetadata{Format: backupManifestFormat(op.Backups), InstanceID: cfg.InstanceID, Configuration: cfg, Source: op.Previous, Checkpoint: record})
}

func allocatedBytes(info fs.FileInfo) int64 {
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0
	}
	blocks := value.FieldByName("Blocks")
	if blocks.IsValid() && blocks.CanInt() {
		return blocks.Int() * 512
	}
	return 0
}
func regularSize(path string) (int64, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0, errors.New("updater: backup file is not regular")
	}
	return info.Size(), allocatedBytes(info), nil
}
func readCheckpoint(cfg Config, dir string) (protocol.BackupCheckpoint, error) {
	id, err := backupID(cfg, dir)
	if err != nil {
		return protocol.BackupCheckpoint{}, err
	}
	record := protocol.BackupCheckpoint{ID: id, Directory: dir, Class: "checkpoint", Unknown: true, Decision: "protected"}
	info, err := os.Lstat(dir)
	if err != nil {
		return record, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return record, errors.New("updater: unsafe checkpoint directory")
	}
	record.CreatedAtMillis = info.ModTime().UnixMilli()
	raw, err := os.ReadFile(filepath.Join(dir, "files.json"))
	if err != nil {
		return record, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&record.Files); err != nil {
		return record, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return record, errors.New("updater: trailing checkpoint manifest content")
	}
	record.ManifestSHA256 = digestRaw(raw)
	metadata, err := os.ReadFile(filepath.Join(dir, backupMetadataName))
	if err == nil {
		var saved backupMetadata
		if err = json.Unmarshal(metadata, &saved); err != nil {
			return record, err
		}
		if (saved.Format != 1 && saved.Format != 2) || saved.Format < backupManifestFormat(record.Files) || saved.InstanceID != cfg.InstanceID || saved.Checkpoint.ID != id || saved.Checkpoint.ManifestSHA256 != record.ManifestSHA256 || digest(saved.Configuration.DataFiles) != digest(cfg.DataFiles) {
			return record, errors.New("updater: checkpoint metadata identity differs")
		}
		files := record.Files
		record = saved.Checkpoint
		record.Directory = dir
		record.Files = files
	} else if !errors.Is(err, os.ErrNotExist) {
		return record, err
	} else if backupManifestFormat(record.Files) != 1 {
		return record, errors.New("updater: multipart backup requires versioned checkpoint metadata")
	}
	for _, f := range record.Files {
		if sharedBackupFile(f) {
			if !f.Existed {
				return record, errors.New("updater: missing file has shared backup objects")
			}
			if _, err := backupParts(cfg, f); err != nil {
				return record, err
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return record, err
	}
	allowed := map[string]bool{"files.json": true, backupMetadataName: true}
	for _, f := range record.Files {
		if f.Existed && !sharedBackupFile(f) {
			allowed[f.Name] = true
		}
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return record, errors.New("updater: unclassified checkpoint file")
		}
		size, allocated, err := regularSize(filepath.Join(dir, entry.Name()))
		if err != nil {
			return record, err
		}
		record.Bytes += size
		record.AllocatedBytes += allocated
	}
	return record, nil
}
func digestRaw(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

func (s *Supervisor) backupProtectedDirectories() map[string]string {
	protected := map[string]string{}
	mark := func(op *Operation) {
		if op != nil && !done(op) && op.BackupDir != "" {
			protected[op.BackupDir] = "unfinished operation owns recovery checkpoint"
		}
	}
	mark(s.state.Operation)
	for _, op := range s.state.OperationHistory {
		saved := op
		mark(&saved)
	}
	if m := s.state.Maintenance; !maintenanceTerminal(m) && m != nil {
		protected[filepath.Join(s.cfg.StateDir, "maintenance-backups", m.Plan.MaintenanceID)] = "unfinished maintenance owns recovery checkpoint"
	}
	return protected
}
func (s *Supervisor) unresolvedBackupResponsibility() bool {
	return s.persistenceErr != nil || s.selfUpdating || s.state.Recovery != nil && s.state.Recovery.Phase != "ledger_linked" || s.state.Bounded != nil && s.state.Bounded.Phase != "complete" && s.state.Bounded.Phase != "cancelled"
}

func (s *Supervisor) backupPreview(ctx context.Context, policy protocol.BackupPolicy) (protocol.BackupPreview, error) {
	p, err := normalizeBackupPolicy(policy)
	if err != nil {
		return protocol.BackupPreview{}, err
	}
	out := protocol.BackupPreview{Format: 1, InstanceID: s.cfg.InstanceID, ExpectedGeneration: s.state.Generation, CreatedAtMillis: s.opts.Now().UnixMilli(), Policy: p, Checkpoints: []protocol.BackupCheckpoint{}, Objects: []protocol.BackupObject{}}
	protected := s.backupProtectedDirectories()
	unknownReferences := false
	for _, root := range []string{"backups", "maintenance-backups"} {
		entries, err := os.ReadDir(filepath.Join(s.cfg.StateDir, root))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return out, err
			}
			dir := filepath.Join(s.cfg.StateDir, root, entry.Name())
			record, readErr := readCheckpoint(s.cfg, dir)
			if readErr != nil {
				record.Unknown = true
				unknownReferences = true
				record.Reasons = append(record.Reasons, "checkpoint manifest or content inventory cannot be verified")
			}
			if reason := protected[dir]; reason != "" {
				record.Reasons = append(record.Reasons, reason)
			}
			if s.unresolvedBackupResponsibility() {
				record.Reasons = append(record.Reasons, "updater retains unresolved recovery responsibility")
			}
			if record.ProtectedReason != "" {
				record.Reasons = append(record.Reasons, "explicit protection: "+record.ProtectedReason)
			}
			if record.Unknown || record.VerifiedAtMillis == 0 || !record.Recoverable && record.Class != "failed_drill" {
				record.Reasons = append(record.Reasons, "no verified completed restore proof")
			}
			out.Checkpoints = append(out.Checkpoints, record)
		}
	}
	sort.Slice(out.Checkpoints, func(i, j int) bool {
		a, b := out.Checkpoints[i], out.Checkpoints[j]
		if a.CreatedAtMillis == b.CreatedAtMillis {
			return a.ID < b.ID
		}
		return a.CreatedAtMillis > b.CreatedAtMillis
	})
	daily, weekly := map[string]bool{}, map[string]bool{}
	verified := 0
	for i := range out.Checkpoints {
		r := &out.Checkpoints[i]
		if r.Recoverable && !r.Unknown && r.VerifiedAtMillis > 0 && r.Class != "successful_drill" && r.Class != "failed_drill" {
			verified++
			if out.RecoveryCheckpointID == "" {
				out.RecoveryCheckpointID = r.ID
			}
			if verified <= p.MinimumVerified {
				r.Reasons = append(r.Reasons, "latest verified recovery checkpoint")
			}
			date := time.UnixMilli(r.CreatedAtMillis).UTC()
			day := date.Format("2006-01-02")
			year, week := date.ISOWeek()
			weekKey := fmt.Sprintf("%d-%02d", year, week)
			if !daily[day] && len(daily) < p.Daily {
				daily[day] = true
				r.Reasons = append(r.Reasons, "daily retention bucket")
			}
			if !weekly[weekKey] && len(weekly) < p.Weekly {
				weekly[weekKey] = true
				r.Reasons = append(r.Reasons, "weekly retention bucket")
			}
		}
		now := out.CreatedAtMillis
		age := now - r.CreatedAtMillis
		switch r.Class {
		case "migration":
			days := p.MigrationDays
			if r.IncompatibleSchema {
				days = p.IncompatibleDays
			}
			if r.ObservationCompletedMillis == 0 || r.ObservationCompletedMillis < r.CreatedAtMillis+int64(p.MigrationDays)*int64(24*time.Hour/time.Millisecond) || age < int64(days)*int64(24*time.Hour/time.Millisecond) {
				r.Reasons = append(r.Reasons, "migration restore observation window")
			}
		case "successful_drill":
			if age < int64(p.SuccessfulDrillHours)*int64(time.Hour/time.Millisecond) {
				r.Reasons = append(r.Reasons, "successful restore drill retention window")
			}
		case "failed_drill":
			if age < int64(p.FailedDrillDays)*int64(24*time.Hour/time.Millisecond) {
				r.Reasons = append(r.Reasons, "explained failed drill retention window")
			}
		case "checkpoint":
		default:
			r.Reasons = append(r.Reasons, "unrecognized checkpoint class")
		}
		r.Decision = "delete"
		if len(r.Reasons) > 0 {
			r.Decision = "keep"
			if r.Unknown || r.ProtectedReason != "" || protected[r.Directory] != "" || s.unresolvedBackupResponsibility() {
				r.Decision = "protected"
			}
		}
	}
	// A failed exercise's disposable copy can go only while the proven
	// covering recovery checkpoint remains available through this batch.
	for i := range out.Checkpoints {
		r := &out.Checkpoints[i]
		if r.Class != "failed_drill" || r.CoveredByCheckpointID == "" {
			continue
		}
		cover := -1
		for k := range out.Checkpoints {
			if out.Checkpoints[k].ID == r.CoveredByCheckpointID {
				cover = k
				break
			}
		}
		if cover < 0 || !out.Checkpoints[cover].Recoverable || out.Checkpoints[cover].Unknown {
			r.Decision = "protected"
			r.Reasons = append(r.Reasons, "covering recovery checkpoint is unavailable")
		} else {
			c := &out.Checkpoints[cover]
			if c.Decision == "delete" {
				c.Decision = "keep"
			}
			c.Reasons = append(c.Reasons, "covers explained restore exercise")
		}
	}
	// Shared backup objects are rooted by every retained checkpoint, including
	// unknown manifests. Online originals remain in their separate fixed roots.
	roots := map[string]bool{}
	unknown := unknownReferences
	for _, r := range out.Checkpoints {
		if r.Decision == "delete" {
			out.ExpectedReclaimedBytes += r.Bytes
			out.ExpectedReclaimedAllocatedBytes += r.AllocatedBytes
			continue
		}
		for _, f := range r.Files {
			if f.ObjectSHA256 != "" {
				roots[f.ObjectSHA256] = true
			}
			for _, part := range f.ObjectParts {
				roots[part.SHA256] = true
			}
		}
	}
	objectRoot := filepath.Join(s.cfg.StateDir, "backup-objects")
	err = filepath.WalkDir(objectRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) && path == objectRoot {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		size, allocated, err := regularSize(path)
		if err != nil {
			return err
		}
		key := entry.Name()
		if !validBackupSHA256(key) || filepath.Dir(path) != filepath.Join(objectRoot, key[:2]) {
			return nil
		}
		obj := protocol.BackupObject{SHA256: key, Bytes: size, AllocatedBytes: allocated, Decision: "delete", Reasons: []string{}}
		if roots[key] {
			obj.Decision = "keep"
			obj.Reasons = append(obj.Reasons, "retained checkpoint reference")
		}
		if unknown {
			obj.Decision = "protected"
			obj.Reasons = append(obj.Reasons, "unknown checkpoint may reference this object")
		}
		if obj.Decision == "delete" {
			out.ExpectedReclaimedBytes += size
			out.ExpectedReclaimedAllocatedBytes += allocated
		}
		out.Objects = append(out.Objects, obj)
		return nil
	})
	if err != nil {
		return out, err
	}
	sort.Slice(out.Objects, func(i, j int) bool { return out.Objects[i].SHA256 < out.Objects[j].SHA256 })
	out.DiagnosticLogs, err = s.diagnosticLogPreview(ctx, p)
	if err != nil {
		return out, err
	}
	for _, r := range out.DiagnosticLogs {
		if r.Decision == "delete" {
			out.ExpectedReclaimedBytes += r.Bytes
			out.ExpectedReclaimedAllocatedBytes += r.AllocatedBytes
		}
	}
	out.SHA256 = backupPreviewDigest(out)
	return out, nil
}
func backupPreviewDigest(p protocol.BackupPreview) string {
	p.SHA256 = ""
	p.CreatedAtMillis = 0
	p.DiagnosticLogs = slices.Clone(p.DiagnosticLogs)
	for i := range p.DiagnosticLogs {
		if !p.DiagnosticLogs[i].Closed {
			p.DiagnosticLogs[i].Bytes = 0
			p.DiagnosticLogs[i].AllocatedBytes = 0
		}
	}
	return digest(p)
}

func (s *Supervisor) BackupPreview(ctx context.Context, policy protocol.BackupPolicy) (protocol.BackupPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backupPreview(ctx, policy)
}
func (s *Supervisor) BackupStatus(ctx context.Context) (protocol.BackupStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.backupPreview(ctx, protocol.BackupPolicy{})
	state := cloneState(s.state)
	return protocol.BackupStatus{Preview: p, Prune: state.BackupPrune}, err
}

func (s *Supervisor) VerifyBackup(ctx context.Context, input protocol.BackupVerification) (protocol.BackupCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return protocol.BackupCheckpoint{}, err
	}
	if input.ExpectedGeneration != s.state.Generation {
		return protocol.BackupCheckpoint{}, errors.New("updater: backup verification generation changed")
	}
	dir, err := backupDir(s.cfg, input.CheckpointID)
	if err != nil {
		return protocol.BackupCheckpoint{}, err
	}
	record, err := readCheckpoint(s.cfg, dir)
	if err != nil {
		return record, err
	}
	if input.ManifestSHA256 != record.ManifestSHA256 {
		return record, errors.New("updater: checkpoint manifest changed")
	}
	if err = ctx.Err(); err != nil {
		return record, err
	}
	if err = verifyBackupFiles(s.cfg, dir, record.Files); err != nil {
		return record, err
	}
	record.VerifiedAtMillis = s.opts.Now().UnixMilli()
	record.Class = input.Class
	if record.Class == "" {
		record.Class = "checkpoint"
	}
	if !slices.Contains([]string{"checkpoint", "migration", "successful_drill", "failed_drill"}, record.Class) {
		return record, errors.New("updater: invalid checkpoint class")
	}
	record.ProtectedReason = input.ProtectedReason
	record.IncompatibleSchema = input.IncompatibleSchema
	record.ObservationCompletedMillis = input.ObservationCompletedMillis
	if record.ObservationCompletedMillis > record.VerifiedAtMillis {
		return record, errors.New("updater: observation completion is in the future")
	}
	record.Unknown = true
	record.Recoverable = false
	var acceptedProof *protocol.BackupRestoreProof
	if input.RestoreProofPath != "" || input.RestoreProofSHA256 != "" {
		if !filepath.IsAbs(input.RestoreProofPath) || !validBackupSHA256(input.RestoreProofSHA256) {
			return record, errors.New("updater: complete restore proof path and SHA256 required")
		}
		raw, err := os.ReadFile(input.RestoreProofPath)
		if err != nil {
			return record, err
		}
		hash := digestRaw(raw)
		if hash != input.RestoreProofSHA256 {
			return record, errors.New("updater: restore proof bytes changed")
		}
		var proof protocol.BackupRestoreProof
		if err = json.Unmarshal(raw, &proof); err != nil {
			return record, err
		}
		acceptedProof = &proof
		if proof.Format != 1 || proof.CheckpointID != record.ID || proof.ManifestSHA256 != record.ManifestSHA256 || proof.VerifiedAtMillis <= 0 || proof.VerifiedAtMillis > record.VerifiedAtMillis {
			return record, errors.New("updater: completed restore proof does not match checkpoint")
		}
		if record.Class == "failed_drill" {
			if proof.Restored || !proof.Explained || !validBackupSHA256(proof.UniqueEvidenceSHA256) || proof.CoveredByCheckpointID == record.ID {
				return record, errors.New("updater: failed drill requires explained unique evidence and a separate recovery checkpoint")
			}
			coverDir, err := backupDir(s.cfg, proof.CoveredByCheckpointID)
			if err != nil {
				return record, err
			}
			cover, err := readCheckpoint(s.cfg, coverDir)
			if err != nil {
				return record, err
			}
			if !cover.Recoverable || cover.Unknown || cover.VerifiedAtMillis == 0 {
				return record, errors.New("updater: failed drill has no verified covering checkpoint")
			}
			if err = verifyBackupFiles(s.cfg, coverDir, cover.Files); err != nil {
				return record, err
			}
			if !filepath.IsAbs(proof.UniqueEvidencePath) {
				return record, errors.New("updater: failed drill requires readable unique evidence")
			}
			evidenceStore, err := blobstore.New(filepath.Join(s.cfg.StateDir, "backup-evidence", "originals"))
			if err != nil {
				return record, err
			}
			evidenceInfo, err := os.Lstat(proof.UniqueEvidencePath)
			if err != nil {
				return record, err
			}
			if !evidenceInfo.Mode().IsRegular() || evidenceInfo.Mode()&os.ModeSymlink != 0 {
				return record, errors.New("updater: unique exercise evidence is not a regular file")
			}
			evidence, err := os.Open(proof.UniqueEvidencePath)
			if err != nil {
				return record, err
			}
			preserved, putErr := evidenceStore.Put(ctx, evidence)
			closeErr := evidence.Close()
			if err = errors.Join(putErr, closeErr); err != nil {
				return record, err
			}
			if preserved.SHA256 != proof.UniqueEvidenceSHA256 {
				return record, errors.New("updater: unique exercise evidence differs from its proof")
			}
			record.CoveredByCheckpointID = cover.ID
		} else if !proof.Restored {
			return record, errors.New("updater: checkpoint has no successful restore proof")
		}
		record.RestoreProofSHA256 = hash
		record.Unknown = false
		record.Recoverable = proof.Restored
		proofRoot := filepath.Join(s.cfg.StateDir, "backup-evidence")
		if err = os.MkdirAll(proofRoot, 0700); err != nil {
			return record, err
		}
		// Compact exercise evidence remains after its disposable work copy.
		if err = atomicWrite(filepath.Join(proofRoot, hash+".json"), raw, 0600); err != nil {
			return record, err
		}
	}
	record.Bytes = 0
	record.AllocatedBytes = 0
	record.Decision = ""
	record.Reasons = nil
	metadata := backupMetadata{Format: backupManifestFormat(record.Files), InstanceID: s.cfg.InstanceID, Configuration: s.cfg, Checkpoint: record}
	if prior, err := os.ReadFile(filepath.Join(dir, backupMetadataName)); err == nil {
		var saved backupMetadata
		if err = json.Unmarshal(prior, &saved); err != nil {
			return record, err
		}
		metadata.Source = saved.Source
		metadata.Configuration = saved.Configuration
	}
	if acceptedProof != nil {
		proof := *acceptedProof
		if metadata.Source.Artifact.SHA256 != "" && proof.SourceArtifactSHA256 != "" && metadata.Source.Artifact.SHA256 != proof.SourceArtifactSHA256 {
			return record, errors.New("updater: restore proof changes historical source")
		}
		if metadata.Source.Artifact.SHA256 == "" {
			if !validBackupSHA256(proof.SourceArtifactSHA256) || !validBackupSHA256(proof.SourceManifestSHA256) || proof.SourceReleaseID == "" || proof.SourceProtocol <= 0 || proof.SourceStorageVersion <= 0 {
				return record, errors.New("updater: legacy restore proof must identify its actual source components")
			}
			metadata.Source = Version{Protocol: proof.SourceProtocol, StorageVersion: proof.SourceStorageVersion, ReleaseID: proof.SourceReleaseID, ManifestSHA256: proof.SourceManifestSHA256}
			metadata.Source.Artifact.SHA256 = proof.SourceArtifactSHA256
			metadata.Source.Artifact.Role = s.cfg.Role
		}
	}
	if err = writeJSON(filepath.Join(dir, backupMetadataName), metadata); err != nil {
		return record, err
	}
	if err = s.save(); err != nil {
		return record, err
	}
	return record, nil
}

func (s *Supervisor) PruneBackups(ctx context.Context, input protocol.BackupPruneRequest) (protocol.BackupPruneState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return protocol.BackupPruneState{}, err
	}
	requested := input.Preview
	if requested.Format != 1 || requested.InstanceID != s.cfg.InstanceID || requested.SHA256 == "" || backupPreviewDigest(requested) != requested.SHA256 {
		return protocol.BackupPruneState{}, errors.New("updater: invalid original backup preview")
	}
	state := s.state.BackupPrune
	if state != nil && state.Preview.SHA256 == requested.SHA256 && state.Phase == "completed" {
		return *state, nil
	}
	if state != nil && state.Phase != "completed" && state.Preview.SHA256 != requested.SHA256 {
		return *state, errors.New("updater: original prune remains unresolved")
	}
	fresh, err := s.backupPreview(ctx, requested.Policy)
	if err != nil {
		return protocol.BackupPruneState{}, err
	}
	if state == nil || state.Phase == "completed" {
		if requested.SHA256 != fresh.SHA256 || requested.ExpectedGeneration != s.state.Generation {
			return protocol.BackupPruneState{}, errors.New("updater: backup preview changed; preview again before pruning")
		}
		backupDeletes := false
		for _, r := range fresh.Checkpoints {
			backupDeletes = backupDeletes || r.Decision == "delete"
		}
		for _, o := range fresh.Objects {
			backupDeletes = backupDeletes || o.Decision == "delete"
		}
		if backupDeletes && fresh.RecoveryCheckpointID == "" {
			return protocol.BackupPruneState{}, errors.New("updater: no verified retained recovery checkpoint")
		}
		state = &protocol.BackupPruneState{Preview: requested, Phase: "pruning", StartedAtMillis: s.opts.Now().UnixMilli(), Completed: []string{}}
		s.state.BackupPrune = state
		if err = s.save(); err != nil {
			return *state, err
		}
	}
	fail := func(err error) (protocol.BackupPruneState, error) {
		state.LastError = err.Error()
		if saveErr := s.save(); saveErr != nil {
			return *state, errors.Join(err, saveErr)
		}
		return *state, err
	}
	backupMutation := false
	for _, r := range requested.Checkpoints {
		backupMutation = backupMutation || r.Decision == "delete" && !slices.Contains(state.Completed, r.ID)
	}
	for _, r := range requested.Objects {
		backupMutation = backupMutation || r.Decision == "delete" && !slices.Contains(state.Completed, "object:"+r.SHA256)
	}
	if backupMutation && s.unresolvedBackupResponsibility() {
		return fail(errors.New("updater: unresolved recovery blocks backup pruning"))
	}
	// A retained restore proof is necessary but content can subsequently be
	// damaged. Verify every retained recovery checkpoint before deleting any.
	for _, r := range fresh.Checkpoints {
		if backupMutation && r.Decision != "delete" && r.Recoverable && !r.Unknown {
			if err = verifyBackupFiles(s.cfg, r.Directory, r.Files); err != nil {
				return fail(err)
			}
		}
	}
	current := map[string]protocol.BackupCheckpoint{}
	for _, r := range fresh.Checkpoints {
		current[r.ID] = r
	}
	for _, r := range requested.Checkpoints {
		if r.Decision != "delete" || slices.Contains(state.Completed, r.ID) {
			continue
		}
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		actual, exists := current[r.ID]
		if exists && state.InFlight != r.ID && (actual.Decision != "delete" || actual.ManifestSHA256 != r.ManifestSHA256) {
			return fail(errors.New("updater: checkpoint became protected or changed"))
		}
		dir, err := backupDir(s.cfg, r.ID)
		if err != nil {
			return fail(err)
		}
		if state.InFlight != r.ID {
			if err = verifyBackupFiles(s.cfg, dir, r.Files); err != nil {
				return fail(err)
			}
			state.InFlight = r.ID
			if err = s.save(); err != nil {
				return *state, err
			}
		}
		if err = removeCheckpoint(dir, r); err != nil {
			return fail(err)
		}
		state.InFlight = ""
		state.Completed = append(state.Completed, r.ID)
		state.ReclaimedBytes += r.Bytes
		state.ReclaimedAllocatedBytes += r.AllocatedBytes
		if err = s.save(); err != nil {
			return *state, err
		}
	}
	// Re-read the complete retained manifest union after removing checkpoints.
	// Newly retained/unknown roots take precedence over an old object candidate.
	fresh, err = s.backupPreview(ctx, requested.Policy)
	if err != nil {
		return fail(err)
	}
	objects := map[string]protocol.BackupObject{}
	for _, r := range fresh.Objects {
		objects[r.SHA256] = r
	}
	store, err := backupObjectStore(s.cfg)
	if err != nil {
		return fail(err)
	}
	for _, object := range requested.Objects {
		key := "object:" + object.SHA256
		if object.Decision != "delete" || slices.Contains(state.Completed, key) {
			continue
		}
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		actual, exists := objects[object.SHA256]
		if exists && (actual.Decision != "delete" || actual.Bytes != object.Bytes) {
			return fail(errors.New("updater: shared backup object became protected or changed"))
		}
		path, err := store.Path(object.SHA256)
		if err != nil {
			return fail(err)
		}
		if exists {
			hash, err := hashFile(path)
			if err != nil || hash != object.SHA256 {
				return fail(errors.New("updater: shared backup object changed"))
			}
		}
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(err)
		}
		if err = syncDir(filepath.Dir(path)); err != nil {
			return fail(err)
		}
		state.Completed = append(state.Completed, key)
		state.ReclaimedBytes += object.Bytes
		state.ReclaimedAllocatedBytes += object.AllocatedBytes
		if err = s.save(); err != nil {
			return *state, err
		}
	}
	for _, record := range requested.DiagnosticLogs {
		if record.Decision != "compress" && record.Decision != "delete" {
			continue
		}
		key := "log:" + record.Decision + ":" + record.ID
		if slices.Contains(state.Completed, key) {
			continue
		}
		state.InFlight = key
		if err = s.save(); err != nil {
			return *state, err
		}
		bytes, allocated, err := s.pruneDiagnosticLog(ctx, record)
		if err != nil {
			return fail(err)
		}
		state.Completed = append(state.Completed, key)
		state.InFlight = ""
		state.ReclaimedBytes += bytes
		state.ReclaimedAllocatedBytes += allocated
		if err = s.save(); err != nil {
			return *state, err
		}
	}
	state.Phase = "completed"
	state.LastError = ""
	state.FinishedAtMillis = s.opts.Now().UnixMilli()
	if err = s.save(); err != nil {
		return *state, err
	}
	return *state, nil
}

func removeCheckpoint(dir string, r protocol.BackupCheckpoint) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	allowed := map[string]bool{"files.json": true, backupMetadataName: true}
	for _, f := range r.Files {
		if f.Existed && !sharedBackupFile(f) {
			allowed[f.Name] = true
		}
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] || !entry.Type().IsRegular() {
			return errors.New("updater: checkpoint directory changed during prune")
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == backupMetadataName {
			continue
		}
		expected := r.ManifestSHA256
		if name != "files.json" {
			for _, f := range r.Files {
				if f.Name == name {
					expected = f.SHA256
					break
				}
			}
		}
		hash, err := hashFile(filepath.Join(dir, name))
		if err != nil || hash != expected {
			return errors.New("updater: remaining prune input changed")
		}
	}
	// Manifest deletion is last, so a partial deletion remains recognizable as
	// the original prune instead of looking like a new empty checkpoint.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	for _, entry := range entries {
		if entry.Name() == "files.json" {
			continue
		}
		if err = os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	if err = os.Remove(filepath.Join(dir, "files.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = os.Remove(dir); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dir))
}

// RetainedBackupOriginals exposes fixed restore paths, including spool objects,
// for the server's online + backup root union. Unreadable metadata is an error,
// never permission to collect an unobserved original.
func RetainedBackupOriginals(cfg Config) ([]BackupFile, error) {
	var files []BackupFile
	for _, root := range []string{"backups", "maintenance-backups"} {
		entries, err := os.ReadDir(filepath.Join(cfg.StateDir, root))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			record, err := readCheckpoint(cfg, filepath.Join(cfg.StateDir, root, entry.Name()))
			if err != nil {
				return nil, err
			}
			for _, f := range record.Files {
				if f.Existed && configuredOriginal(cfg, f.Path) {
					files = append(files, f)
				}
			}
		}
	}
	return files, nil
}

func validBackupSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && strings.ToLower(s) == s
}
