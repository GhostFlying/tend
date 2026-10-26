package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (r *Runner) observationReader(runCtx context.Context, as protocol.Assignment) func(context.Context) (protocol.RunObservation, error) {
	var spec struct {
		Observation *protocol.ObservationScope `json:"observation"`
	}
	if json.Unmarshal(as.Spec, &spec) != nil || spec.Observation == nil {
		return nil
	}
	return func(ctx context.Context) (protocol.RunObservation, error) {
		callCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(runCtx, cancel)
		defer stop()
		if runCtx.Err() != nil {
			cancel()
		}
		return r.readObservation(callCtx, as)
	}
}

func (r *Runner) readObservation(ctx context.Context, as protocol.Assignment) (protocol.RunObservation, error) {
	var out protocol.RunObservation
	if r.c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.c.timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.c.base+"/runner/v1/observation", nil)
	if err != nil {
		return out, fmt.Errorf("build observation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+as.RunToken)
	req.Header.Set(headerRun, as.RunID)
	resp, err := r.c.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("read observation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("observation status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("decode observation: %w", err)
	}
	return out, nil
}
