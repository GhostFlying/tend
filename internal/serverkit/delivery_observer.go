package serverkit

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/reviewwatch"
)

// DeliveryObserverConfig selects native provider CLIs on the server. Credentials
// remain in those CLIs' existing login stores, never in Tend configuration.
type DeliveryObserverConfig struct {
	Providers   []string         `json:"providers"`
	GitHubBin   string           `json:"gh_bin,omitempty"`
	CodebaseBin string           `json:"bytedcli_bin,omitempty"`
	Timeout     cfgfile.Duration `json:"timeout,omitempty"`
}

type DeliveryObserver interface {
	Observe(context.Context, reviewwatch.Ref) (reviewwatch.Observation, error)
}

func (c *DeliveryObserverConfig) validate() error {
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, p := range c.Providers {
		path := c.GitHubBin
		if p == reviewwatch.Codebase {
			path = c.CodebaseBin
		} else if p != reviewwatch.GitHub {
			return errors.New("unsupported server delivery provider")
		}
		if seen[p] {
			return errors.New("duplicate server delivery provider")
		}
		seen[p] = true
		if !filepath.IsAbs(path) {
			return errors.New("server delivery provider requires an absolute native executable path")
		}
	}
	timeout := time.Duration(c.Timeout)
	if timeout != 0 && (timeout < time.Second || timeout > time.Minute) {
		return errors.New("server delivery timeout must be between one second and one minute")
	}
	return nil
}

func runDeliveryObserver(ctx context.Context, a *app.App, c DeliveryObserverConfig, observer DeliveryObserver, log *slog.Logger) {
	if observer == nil {
		observer = reviewwatch.New(reviewwatch.Config{IncludeChecks: true, GitHubBin: c.GitHubBin, CodebaseBin: c.CodebaseBin, Timeout: time.Duration(c.Timeout)})
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := observeServerDelivery(ctx, a, c, observer); err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "server delivery observation deferred", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func observeServerDelivery(ctx context.Context, a *app.App, c DeliveryObserverConfig, observer DeliveryObserver) error {
	q, err := a.ClaimServerDelivery(ctx)
	if err != nil || q == nil {
		return err
	}
	kind := ""
	var snapshot *protocol.DeliverySnapshot
	if !slices.Contains(c.Providers, q.Provider) {
		kind = "provider_not_configured"
	} else {
		timeout := time.Duration(c.Timeout)
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		readCtx, cancel := context.WithTimeout(ctx, timeout)
		observed, readErr := observer.Observe(readCtx, reviewwatch.Ref{Provider: q.Provider, Repository: q.Repository, Number: q.Number})
		cancel()
		if readErr != nil {
			kind = "query_failed"
			var failure *reviewwatch.Error
			if errors.As(readErr, &failure) {
				kind = failure.Kind
			} else if errors.Is(readErr, context.DeadlineExceeded) {
				kind = "timeout"
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else {
			snapshot = &protocol.DeliverySnapshot{LineageErrorKind: observed.LineageErrorKind, Mergeability: observed.Mergeability, CI: observed.CI, TargetSHA: observed.TargetSHA, TargetContainsMerge: observed.TargetContainsMerge, BaseSHA: observed.BaseSHA, State: observed.State, HeadSHA: observed.HeadSHA, MergeSHA: observed.MergeSHA, URL: observed.URL, ProviderUpdatedAt: observed.UpdatedAt, MergedAt: observed.MergedAt, ClosedAt: observed.ClosedAt}
			if observed.MergedBy != nil {
				snapshot.MergedBy = &protocol.DeliveryActor{ID: observed.MergedBy.ID, Login: observed.MergedBy.Login}
			}
			if observed.ClosedBy != nil {
				snapshot.ClosedBy = &protocol.DeliveryActor{ID: observed.ClosedBy.ID, Login: observed.ClosedBy.Login}
			}
		}
	}
	// Recording retries preserve the same request identity and observed payload;
	// a lost reply must never cause another external query in this attempt.
	for attempt := 0; attempt < 3; attempt++ {
		saveCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		err = a.RecordServerDelivery(saveCtx, q.ID, snapshot, kind)
		cancel()
		if err == nil || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return err
}

// deliveryProviderAvailable is configuration diagnosis, not authentication or
// provider access proof; a real object query supplies those facts separately.
func deliveryProviderAvailable(path string) bool { _, err := exec.LookPath(path); return err == nil }
