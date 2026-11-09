package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
)

func (b *Broker) trackUsage(run string, source nativeusage.Source, stopped bool) {
	if b.cfg.UsageSource == nil || source.Path == "" {
		return
	}
	b.usageMu.Lock()
	raw, err := os.ReadFile(b.bindingPath(run))
	var binding nativeBinding
	if err != nil || json.Unmarshal(raw, &binding) != nil || binding.Thread != source.ThreadID {
		b.usageMu.Unlock()
		b.log.Warn("central usage binding requires reconciliation", "run_id", run)
		return
	}
	binding.Usage, binding.UsageStopped = &source, stopped || binding.UsageStopped
	if err := b.saveBinding(run, binding); err != nil {
		b.log.Warn("central usage binding persistence pending", "run_id", run)
	}
	b.mu.Lock()
	b.pendingUsage[run] = binding
	b.mu.Unlock()
	b.usageMu.Unlock()
	b.flushUsage()
}

func (b *Broker) stopUsage(run string) {
	b.usageMu.Lock()
	raw, err := os.ReadFile(b.bindingPath(run))
	var binding nativeBinding
	if err != nil || json.Unmarshal(raw, &binding) != nil {
		b.usageMu.Unlock()
		return
	}
	binding.UsageStopped = true
	if err := b.saveBinding(run, binding); err != nil {
		b.log.Warn("central stop evidence persistence pending", "run_id", run)
	}
	b.usageMu.Unlock()
	if binding.Usage != nil {
		b.trackUsage(run, *binding.Usage, true)
	}
}

func (b *Broker) usageHome() string {
	if b.cfg.Home != "" {
		return b.cfg.Home
	}
	for _, key := range []string{"TRAECLI_HOME", "TRAE_HOME"} {
		if home := os.Getenv(key); home != "" {
			return home
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".trae")
	}
	return ""
}

func (b *Broker) restoreUsage() {
	if b.cfg.RegisteredUsageRuns != nil {
		if runs, err := b.cfg.RegisteredUsageRuns(b.ctx); err == nil {
			for _, run := range runs {
				raw, err := os.ReadFile(b.bindingPath(run))
				var binding nativeBinding
				if err != nil || json.Unmarshal(raw, &binding) != nil || binding.Thread == "" || binding.Pending {
					continue
				}
				binding.RunID = run
				if binding.Usage == nil && b.usageHome() != "" {
					if source, err := nativeusage.Locate(b.ctx, b.usageHome(), "traex", binding.Thread); err == nil {
						binding.Usage = &source
					}
				}
				if binding.Usage != nil {
					_ = b.saveBinding(run, binding)
					b.pendingUsage[run] = binding
				}
			}
		}
	}
	files, err := os.ReadDir(filepath.Join(b.cfg.Cwd, "run-bindings"))
	if err != nil {
		return
	}
	// Run identity is retained alongside source paths so recovery never scans a
	// provider's personal histories to infer Tend ownership.
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(b.cfg.Cwd, "run-bindings", file.Name()))
		var binding nativeBinding
		if err == nil && json.Unmarshal(raw, &binding) == nil && binding.Usage != nil && binding.RunID != "" {
			b.pendingUsage[binding.RunID] = binding
		}
	}
}

func (b *Broker) usageLoop() {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		b.flushUsage()
		select {
		case <-b.ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (b *Broker) flushUsage() {
	b.usageFlushMu.Lock()
	defer b.usageFlushMu.Unlock()
	b.mu.Lock()
	pending := make(map[string]nativeBinding, len(b.pendingUsage))
	for run, binding := range b.pendingUsage {
		pending[run] = binding
	}
	b.mu.Unlock()
	for run, binding := range pending {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), 5*time.Second)
		err := b.cfg.UsageSource(ctx, run, *binding.Usage, binding.UsageStopped)
		cancel()
		if err != nil {
			continue
		}
		b.mu.Lock()
		if current := b.pendingUsage[run]; current.UsageStopped == binding.UsageStopped && current.Usage != nil && current.Usage.Path == binding.Usage.Path {
			delete(b.pendingUsage, run)
		}
		b.mu.Unlock()
	}
}

func (b *Broker) recordUsageDispatch(ctx context.Context, run, thread, request string) error {
	b.usageMu.Lock()
	raw, err := os.ReadFile(b.bindingPath(run))
	var binding nativeBinding
	if err == nil {
		err = json.Unmarshal(raw, &binding)
	}
	if err == nil && (binding.Thread != thread || binding.Usage == nil) {
		err = errors.New("native usage dispatch binding is unavailable")
	}
	if err == nil {
		binding.Usage.NoDispatchProven = false
		err = b.saveBinding(run, binding)
	}
	b.usageMu.Unlock()
	if err != nil {
		return err
	}
	return b.cfg.BeforeUsageDispatch(ctx, run, thread, request)
}
