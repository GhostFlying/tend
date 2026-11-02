package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/rollout"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (r *Runner) registerRollout(ctx context.Context, run string, source protocol.RolloutPreviewSource) error {
	r.controlMu.Lock()
	deployment := r.previewDeployment
	r.controlMu.Unlock()
	if deployment == "" || len(r.cfg.RolloutRoots) == 0 || source.SessionID == "" {
		return nil
	}
	return r.cfg.Journal.RegisterRollout(ctx, deployment, run, source)
}
func (r *Runner) recordRolloutEvent(ctx context.Context, run string, ev harness.WorkerEvent) error {
	// Lifecycle and child identity facts cover new turns without adding a journal
	// transaction to every streamed output delta.
	switch ev.Kind {
	case protocol.EventNativeIdentity, "native.child", "turn.started", "turn.completed", "turn.failed", "native.turn_started", "native.turn_completed":
	default:
		return nil
	}
	var source protocol.RolloutPreviewSource
	if ev.Kind == protocol.EventNativeIdentity {
		var identity protocol.NativeIdentity
		if json.Unmarshal(ev.Body, &identity) != nil || identity.State != "known" {
			return nil
		}
		source.SessionID, source.TurnID = identity.SessionID, identity.TurnID
	} else {
		var identity struct {
			Session string `json:"thread_id"`
			Turn    string `json:"turn_id"`
		}
		if json.Unmarshal(ev.Body, &identity) != nil {
			return nil
		}
		source.SessionID, source.TurnID = identity.Session, identity.Turn
	}
	if source.SessionID == "" {
		return nil
	}
	if len(r.cfg.RolloutRoots) > 0 {
		r.controlMu.Lock()
		deployment := r.previewDeployment
		r.controlMu.Unlock()
		prior, err := r.cfg.Journal.RolloutSources(ctx, deployment, run)
		if err != nil {
			return err
		}
		for _, known := range prior {
			if known.SessionID == source.SessionID && known.Path != "" {
				source.Path = known.Path
				break
			}
		}
		if source.Path == "" {
			path, err := rollout.Locate(ctx, r.cfg.RolloutRoots, source.SessionID)
			if err != nil {
				return fmt.Errorf("runner: locate native source: %w", err)
			}
			source.Path = path
		}
	}
	return r.registerRollout(ctx, run, source)
}

func (r *Runner) previewHTTP(ctx context.Context, method string, query url.Values, body any, result any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, r.c.base+"/runner/v1/rollout-preview?"+query.Encode(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.c.token)
	req.Header.Set(headerHost, r.c.hostID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("runner: preview transport status %d", resp.StatusCode)
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(result)
	}
	return nil
}

// rolloutPreviews neither claims work nor uses an execution lease or model process.
func (r *Runner) rolloutPreviews(ctx context.Context) {
	reader := rollout.New(r.instanceID, r.cfg.RolloutRoots...)
	var wg sync.WaitGroup
	var mu sync.Mutex
	active := map[string]context.CancelFunc{}
	defer wg.Wait()
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, cancel := range active {
			cancel()
		}
	}()
	for ctx.Err() == nil {
		r.controlMu.Lock()
		supported, deployment := r.previewSupported, r.previewDeployment
		r.controlMu.Unlock()
		if !supported {
			select {
			case <-ctx.Done():
				return
			case <-r.previewWake:
			}
			continue
		}
		pollCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
		var work *protocol.RolloutPreviewWork
		err := r.previewHTTP(pollCtx, http.MethodGet, url.Values{"instance": {r.instanceID}}, nil, &work)
		cancel()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(r.cfg.RetryDelay):
			}
			continue
		}
		if work == nil {
			continue
		}
		mu.Lock()
		if work.Cancelled {
			if cancel := active[work.RequestID]; cancel != nil {
				cancel()
			}
			mu.Unlock()
			continue
		}
		if work.DeploymentID != deployment || work.InstanceID != r.instanceID || work.RunID == "" || active[work.RequestID] != nil || len(active) >= 4 {
			mu.Unlock()
			continue
		}
		readCtx, stop := context.WithDeadline(ctx, time.UnixMilli(work.DeadlineMillis))
		active[work.RequestID] = stop
		mu.Unlock()
		wg.Go(func() {
			defer stop()
			defer func() { mu.Lock(); delete(active, work.RequestID); mu.Unlock() }()
			result := r.readRollout(readCtx, reader, *work)
			if readCtx.Err() != nil {
				return
			}
			_ = r.previewHTTP(readCtx, http.MethodPost, nil, protocol.RolloutPreviewReply{RequestID: work.RequestID, InstanceID: r.instanceID, Result: result}, nil)
		})
	}
}

func (r *Runner) readRollout(ctx context.Context, reader *rollout.Reader, work protocol.RolloutPreviewWork) protocol.RolloutPreview {
	unavailable := protocol.RolloutPreview{State: "source_unknown"}
	r.controlMu.Lock()
	deployment := r.previewDeployment
	r.controlMu.Unlock()
	if work.DeploymentID != deployment {
		return unavailable
	}
	sources, err := r.cfg.Journal.RolloutSources(ctx, work.DeploymentID, work.RunID)
	if err != nil {
		return unavailable
	}
	// Legacy rows remain after a run finishes; plain sessions still need the exact native run marker.
	if len(sources) == 0 && !work.RequireTurn {
		thread, manager, err := r.cfg.Journal.HistoricalRolloutThread(ctx, work.RunID)
		if err != nil {
			return unavailable
		}
		if !manager && thread != "" {
			var native protocol.NativeIdentity
			if json.Unmarshal([]byte(thread), &native) == nil {
				thread = native.SessionID
			}
			if thread != "" {
				work.Sources = append(work.Sources, protocol.RolloutPreviewSource{SessionID: thread})
			}
		}
	}
	for _, s := range work.Sources {
		s.Path = "" // The server may supply identities, never paths.
		if work.RequireTurn && s.TurnID == "" {
			continue
		}
		if err := r.registerRollout(ctx, work.RunID, s); err != nil {
			return unavailable
		}
	}
	sources, err = r.cfg.Journal.RolloutSources(ctx, work.DeploymentID, work.RunID)
	if err != nil {
		return unavailable
	}
	exact := map[string]bool{}
	paths := map[string]string{}
	for _, s := range sources {
		if s.TurnID != "" {
			exact[s.SessionID] = true
		}
		if s.Path != "" {
			paths[s.SessionID] = s.Path
		}
	}
	selected := []protocol.RolloutPreviewSource{}
	for _, s := range sources {
		if s.TurnID == "" && (work.RequireTurn || exact[s.SessionID]) {
			continue
		}
		if s.Path == "" {
			s.Path = paths[s.SessionID]
		}
		if s.Path == "" {
			s.Path, err = rollout.Locate(ctx, r.cfg.RolloutRoots, s.SessionID)
			if err != nil {
				return protocol.RolloutPreview{State: "timeout"}
			}
		}
		if s.Path == "" {
			return unavailable
		}
		if err = r.registerRollout(ctx, work.RunID, s); err != nil {
			return unavailable
		}
		selected = append(selected, s)
	}
	return reader.Read(ctx, work.RunID, selected, work.Query)
}
