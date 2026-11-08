package codexenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

func (s *Service) Read(id string) (Status, error) {
	return s.readPage(context.Background(), id, "", 100)
}
func (s *Service) readPage(ctx context.Context, id, after string, limit int) (Status, error) {
	dir, err := s.directory(id)
	if err != nil {
		return Status{}, err
	}
	r, err := loadRecord(filepath.Join(dir, "state.json"))
	for id, request := range r.Requests {
		if request.State == "pending" {
			r.Status.PendingRequestIDs = append(r.Status.PendingRequestIDs, id)
		}
	}
	slices.Sort(r.Status.PendingRequestIDs)
	if err == nil {
		r.Status.Adoptions, r.Status.AdoptionsNext, r.Status.AdoptionsMore, err = adoptionPage(ctx, dir, after, limit)
		for key, adoption := range r.Status.Adoptions {
			if adoption.Generation != r.Status.Latest {
				adoption.PendingGeneration = r.Status.Latest
				adoption.PendingReason = "awaiting_confirmed_idle_and_settled_receipts"
				if strings.HasPrefix(adoption.Execution, "run:") {
					adoption.PendingReason = "run_snapshot_pinned_for_recovery"
				}
				if adoption.BlockedGeneration == r.Status.Latest && r.Status.Revision <= adoption.BlockedRevision {
					adoption.PendingReason = "latest_snapshot_failed_to_start"
				}
				if adoption.State == "unknown" {
					adoption.PendingReason = "process_exit_requires_reconciliation"
				}
				if r.Status.Deleted || !r.Status.Profile.Enabled {
					adoption.PendingReason = "environment_disabled"
				}
			}
			r.Status.Adoptions[key] = adoption
		}
	}
	return r.Status, err
}
func (s *Service) List() ([]Status, error) {
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return []Status{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Status{}
	for _, entry := range entries {
		if !entry.IsDir() || !validID.MatchString(entry.Name()) {
			continue
		}
		record, err := loadRecord(filepath.Join(s.Root, entry.Name(), "state.json"))
		status := record.Status
		if err == ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		if status.Profile.ID == "" {
			continue
		}
		out = append(out, status)
	}
	return out, nil
}
func (s *Service) Execute(ctx context.Context, q Request) (Result, error) {
	if q.Limit < 0 || q.Limit > 100 {
		return Result{}, fmt.Errorf("page size must be 1..100")
	}
	if q.Action == "list" {
		all, err := s.List()
		return Result{Environments: all}, err
	}
	if q.Profile != nil && q.ID == "" {
		q.ID = q.Profile.ID
	}
	dir, err := s.directory(q.ID)
	if err != nil {
		return Result{}, err
	}
	readOnly := slices.Contains([]string{"show", "inventory", "plan", "doctor"}, q.Action) || q.DryRun
	if readOnly {
		return s.inspect(ctx, q)
	}
	if q.RequestID == "" {
		return Result{}, fmt.Errorf("request_id is required")
	}
	// Serialize namespace ownership separately from per-environment execution
	// references, including reconciliation of interrupted profile updates.
	if q.Action == "create" || q.Action == "update" || q.Action == "reconcile" {
		release, err := lock(ctx, filepath.Join(s.Root, ".profiles"))
		if err != nil {
			return Result{}, err
		}
		defer release()
	}
	unlock, err := lock(ctx, dir)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	path := filepath.Join(dir, "state.json")
	r, err := loadRecord(path)
	if err != nil && err != ErrNotFound {
		return Result{}, err
	}
	if err == ErrNotFound {
		r = record{Requests: map[string]receipt{}, Status: Status{Adoptions: map[string]Adoption{}}}
	}
	if q.Action == "reconcile" {
		prior, ok := r.Requests[q.RequestID]
		if !ok {
			return Result{}, ErrNotFound
		}
		if prior.State == "failed" {
			return prior.Result, errors.New(prior.Error)
		}
		if prior.State == "completed" {
			return prior.Result, nil
		}
		q = prior.Request
	}
	raw, _ := json.Marshal(q)
	hash := digest(raw)
	if prior, ok := r.Requests[q.RequestID]; ok {
		if prior.Hash != hash {
			return Result{}, ErrConflict
		}
		if prior.State == "failed" {
			return prior.Result, errors.New(prior.Error)
		}
		if prior.State == "completed" {
			return prior.Result, nil
		}
	} else {
		if r.Status.Revision != q.ExpectedRevision {
			return Result{}, ErrConflict
		}
		for _, prior := range r.Requests {
			if prior.State != "completed" && prior.State != "failed" {
				return Result{}, fmt.Errorf("reconcile the original pending request before a new mutation")
			}
		}
		r.Requests[q.RequestID] = receipt{Hash: hash, Request: q, State: "pending"}
		if err = writeJSON(path, r); err != nil {
			return Result{}, err
		}
	}
	// The requested transition and its receipt commit in the same atomic file.
	next := clone(r)
	before := next.Status.Latest
	if err = s.apply(ctx, &next, q); err != nil {
		r.Status.LastError = err.Error()
		state := "failed"
		_, preflight := errors.AsType[*watchPreflightError](err)
		if !preflight && (strings.HasPrefix(q.Action, "watch ") || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			state = "pending"
		}
		view := clone(r.Status)
		view.Snapshots, view.Adoptions = nil, nil
		result := Result{Status: &view, RequestState: state, RequestID: q.RequestID}
		r.Requests[q.RequestID] = receipt{Hash: hash, Request: q, State: state, Error: err.Error(), Result: clone(result)}
		if writeErr := writeJSON(path, r); writeErr != nil {
			return Result{RequestState: "unknown"}, errors.Join(err, writeErr)
		}
		return result, err
	}
	next.Status.Revision++
	view := clone(next.Status)
	view.Snapshots = nil
	view.Adoptions = nil
	result := Result{Status: &view, Changed: before != next.Status.Latest || q.Action != "sync", RequestState: "completed", RequestID: q.RequestID}
	for _, shot := range next.Status.Snapshots {
		if shot.ID == next.Status.Latest {
			copy := shot
			result.Candidate = &copy
			break
		}
	}
	next.Requests[q.RequestID] = receipt{Hash: hash, Request: q, State: "completed", Result: clone(result)}
	if err = writeJSON(path, next); err != nil {
		return Result{RequestState: "unknown"}, err
	}
	return result, nil
}
func (s *Service) apply(ctx context.Context, r *record, q Request) error {
	switch q.Action {
	case "watch enable", "watch disable":
		if err := s.configureWatch(ctx, q.Action == "watch enable"); err != nil {
			return err
		}
		r.Status.WatchEnabled = q.Action == "watch enable"
		return nil
	case "create", "update":
		if q.Profile == nil {
			return fmt.Errorf("complete environment description required")
		}
		if (q.Action == "create" && r.Status.Revision != 0) || (q.Action == "update" && r.Status.Revision == 0) {
			return ErrConflict
		}
		p := defaults(*q.Profile)
		if p.ID != q.ID {
			return ErrConflict
		}
		if err := validate(p); err != nil {
			return err
		}
		if p.ID == "watch-control" {
			return fmt.Errorf("reserved environment identity")
		}
		if q.Action == "update" && !r.Status.Deleted && disablingOnly(r.Status.Profile, p) {
			r.Status.Profile.Enabled = false
			return nil
		}
		var err error
		p.Skills.Installed, err = s.importItems(ctx, p.ID, "skill", p.Skills.Installed)
		if err != nil {
			return err
		}
		p.Plugins.Installed, err = s.importItems(ctx, p.ID, "plugin", p.Plugins.Installed)
		if err != nil {
			return err
		}
		if r.Status.Revision != 0 && (p.Home != r.Status.Profile.Home || p.UserHome != r.Status.Profile.UserHome) {
			return fmt.Errorf("fixed runtime and user homes cannot change")
		}
		if r.Status.Revision != 0 && p.AuthSource != r.Status.Profile.AuthSource {
			return fmt.Errorf("shared auth source is fixed for an environment")
		}
		all, err := s.List()
		if err != nil {
			return err
		}
		for _, other := range all {
			if other.Profile.ID != p.ID && (within(other.Profile.Home, p.Home) || within(p.Home, other.Profile.Home)) {
				return fmt.Errorf("runtime home is already owned by another environment")
			}
		}
		if samePath(p.Home, p.SourceHome) || samePath(p.Home, filepath.Join(p.UserHome, ".codex")) || within(p.UserHome, p.Home) || within(p.Home, s.Root) || within(s.Root, p.Home) {
			return fmt.Errorf("runtime home overlaps its source or environment store")
		}
		r.Status.Profile = p
		r.Status.Deleted = false
		r.Status.SyncHeld = false
	case "delete":
		if r.Status.Revision == 0 {
			return ErrNotFound
		}
		dir, _ := s.directory(q.ID)
		if active, err := activeAdoptions(ctx, dir); err != nil {
			return err
		} else if active {
			return fmt.Errorf("environment has active or unknown execution references")
		}
		r.Status.Deleted = true
		return nil
	case "sync":
		if r.Status.Revision == 0 || r.Status.Deleted {
			return ErrNotFound
		}
		r.Status.SyncHeld = false
	case "rollback":
		for _, shot := range r.Status.Snapshots {
			if shot.ID == q.Generation {
				r.Status.Latest = shot.ID
				r.Status.SyncHeld = true
				r.Status.LastError = ""
				return nil
			}
		}
		return ErrNotFound
	case "skill add", "skill update", "skill remove", "plugin add", "plugin update", "plugin remove":
		if r.Status.Revision == 0 || r.Status.Deleted {
			return ErrNotFound
		}
		sel := &r.Status.Profile.Skills
		if q.Action[:6] == "plugin" {
			sel = &r.Status.Profile.Plugins
		}
		for _, item := range sel.Allow {
			if item.ID == q.Item.ID {
				return fmt.Errorf("projected item must be detached through an environment update")
			}
		}
		index := slices.IndexFunc(sel.Installed, func(i Item) bool { return i.ID == q.Item.ID })
		if q.Action == "skill remove" || q.Action == "plugin remove" {
			if index < 0 {
				return ErrNotFound
			}
			sel.Installed = slices.Delete(sel.Installed, index, index+1)
		} else {
			if q.Item.Path == "" {
				return fmt.Errorf("complete local package path required")
			}
			owned, err := s.importItems(ctx, r.Status.Profile.ID, q.Action[:strings.IndexByte(q.Action, ' ')], []Item{q.Item})
			if err != nil {
				return err
			}
			q.Item = owned[0]
			if index < 0 {
				sel.Installed = append(sel.Installed, q.Item)
			} else {
				sel.Installed[index] = q.Item
			}
		}
		if err := validate(r.Status.Profile); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported environment action")
	}
	_, err := s.publish(ctx, r)
	return err
}
func (s *Service) inspect(ctx context.Context, q Request) (Result, error) {
	status, err := s.readPage(ctx, q.ID, q.After, q.Limit)
	if err == ErrNotFound && q.Profile != nil {
		status = Status{Profile: defaults(*q.Profile)}
		err = nil
	}
	if err != nil {
		return Result{}, err
	}
	result := Result{Status: &status}
	if q.Action == "show" || q.Action == "inventory" {
		return result, nil
	}
	if q.Profile != nil && !status.Deleted && disablingOnly(status.Profile, defaults(*q.Profile)) {
		result.Changed = status.Profile.Enabled
		return result, nil
	}
	if q.Action == "doctor" {
		cache, err := pluginCacheInventory(status.Profile.Home)
		if err != nil {
			return result, err
		}
		result.PluginCache = &cache
		result.Checks = map[string]string{"platform": "ok", "snapshot": "ok", "account_plugins": "account-scoped; activation is local"}
		if err = platformCheck(ctx); err != nil {
			result.Checks["platform"] = err.Error()
		}
		if status.Latest == "" {
			result.Checks["snapshot"] = "unavailable"
		}
		return result, err
	}
	if q.Profile != nil {
		status.Profile = defaults(*q.Profile)
	}
	if err = validate(status.Profile); err != nil {
		return result, err
	}
	// Read-only previews stage outside the managed store and never initialize it.
	tmp, err := os.MkdirTemp("", "tend-codex-plan-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(tmp)
	preview := *s
	preview.Root = tmp
	shot, stage, err := preview.prepare(ctx, status.Profile, status.Revision)
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	result.Candidate = &shot
	result.Changed = shot.ID != status.Latest
	return result, nil
}

func disablingOnly(current, next Profile) bool {
	if next.Enabled {
		return false
	}
	current.Enabled = false
	return reflect.DeepEqual(current, next)
}
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return canonicalPath(a) == canonicalPath(b)
}
func canonicalPath(path string) string {
	path = filepath.Clean(path)
	original := path
	var suffix []string
	for {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				real = filepath.Join(real, suffix[i])
			}
			return real
		}
		parent := filepath.Dir(path)
		if parent == path {
			return original
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}
func within(path, root string) bool {
	rel, err := filepath.Rel(canonicalPath(root), canonicalPath(path))
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && (len(rel) < 3 || rel[:3] != ".."+string(filepath.Separator))
}
