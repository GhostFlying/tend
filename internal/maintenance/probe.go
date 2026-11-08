package maintenance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type Probe struct {
	URL, TokenFile string
	HTTP           *http.Client
}

func (p Probe) get(ctx context.Context, path string, out any) error {
	token, err := os.ReadFile(p.TokenFile)
	if err != nil {
		return errors.New("maintenance: cannot read owner token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+path, nil)
	if err != nil {
		return errors.New("maintenance: invalid server URL")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return errors.New("maintenance: owner API unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("maintenance: owner API HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// DetectIssue deliberately excludes normal waiting, CI, paused and queued work.
// A candidate asks the agent to diagnose; it never writes business blocked state.
func DetectIssue(v protocol.IssueView) []protocol.MaintenanceCase {
	if v.State != protocol.IssueActive {
		return nil
	}
	var found []protocol.MaintenanceCase
	if m := v.Manager; m != nil && m.ControlMode != protocol.ControlPaused && m.ControlMode != protocol.ControlHumanControl {
		if r := m.Recovery; r != nil && slices.Contains([]string{"exhausted", "blocked", "unknown"}, r.Status) {
			found = append(found, protocol.MaintenanceCase{ID: "issue-" + v.IssueID, Source: v.IssueID, Code: "agent_recovery_" + r.Status, Evidence: "tend issue show " + v.IssueID, Fingerprint: digest(r)})
		}
	}
	for _, task := range v.Tasks {
		if task.Lifecycle.Terminal() || task.ControlMode == protocol.ControlPaused || task.ControlMode == protocol.ControlHumanControl || task.Execution == nil {
			continue
		}
		e := task.Execution
		code := ""
		if e.State == protocol.ExecutionUnknown {
			code = "execution_unknown"
		} else if e.Startup != nil && e.Startup.Phase == "failed" {
			code = "startup_failed"
		}
		if code != "" {
			found = append(found, protocol.MaintenanceCase{ID: "task-" + task.TaskID, Source: v.IssueID, Code: code, Evidence: "tend task show " + task.TaskID, Fingerprint: digest(e)})
		}
	}
	return found
}

func (p Probe) Collect(ctx context.Context) ([]protocol.MaintenanceCase, error) {
	if err := p.get(ctx, "/healthz", nil); err != nil {
		return nil, err
	}
	var found []protocol.MaintenanceCase
	after := ""
	seen := map[string]bool{}
	for {
		var page protocol.IssuePage
		if err := p.get(ctx, "/api/v1/issues?limit=100&after="+url.QueryEscape(after), &page); err != nil {
			return nil, err
		}
		for _, v := range page.Items {
			if v.State != protocol.IssueActive {
				continue
			}
			var detail protocol.IssueView
			if err := p.get(ctx, "/api/v1/issues/"+url.PathEscape(v.IssueID), &detail); err != nil {
				return nil, err
			}
			found = append(found, DetectIssue(detail)...)
		}
		if !page.More {
			return found, nil
		}
		if page.Next == "" || seen[page.Next] {
			return nil, errors.New("maintenance: non-progressing issue cursor")
		}
		seen[page.Next] = true
		after = page.Next
	}
}

func (c *Controller) Check(ctx context.Context, p Probe) error {
	c.mu.Lock()
	frozen := c.frozen()
	c.mu.Unlock()
	if frozen {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	candidates, err := p.Collect(probeCtx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fault != nil {
		return c.fault
	}
	if c.frozen() {
		return nil
	}
	now := c.opts.Now().Unix()
	c.state.CheckRequested = false
	c.state.LastCheck = now
	if err != nil {
		c.state.HealthFailures++
		c.state.LastError = err.Error()
		if c.state.HealthFailures >= 2 {
			c.upsert(protocol.MaintenanceCase{ID: "owner-api", Source: "owner-api", Code: "observation_unavailable", Evidence: "tend doctor", Fingerprint: digest(err.Error())}, now)
		}
	} else {
		c.state.HealthFailures = 0
		c.state.LastError = ""
		for i := range c.state.Cases {
			v := &c.state.Cases[i]
			if !slices.ContainsFunc(candidates, func(candidate protocol.MaintenanceCase) bool { return candidate.ID == v.ID }) {
				v.Observed = false
				if v.Status == "open" {
					v.Status = "not_observed"
				}
			}
		}
		for _, v := range candidates {
			c.upsert(v, now)
		}
	}
	if e := c.save(ctx); e != nil {
		return e
	}
	return err
}

func (c *Controller) upsert(v protocol.MaintenanceCase, now int64) {
	for i := range c.state.Cases {
		old := &c.state.Cases[i]
		if old.ID != v.ID {
			continue
		}
		old.LastSeen = now
		observedBefore := old.Observed
		old.Observed = true
		if old.Status == "deferred" && old.DeferUntil > now && old.Fingerprint == v.Fingerprint && old.Code == v.Code {
			return
		}
		if old.Fingerprint == v.Fingerprint && old.Code == v.Code && old.Status != "deferred" && observedBefore {
			return
		}
		v.FirstSeen = old.FirstSeen
		v.LastSeen = now
		v.Status = "open"
		v.Observed = true
		*old = v
		c.enqueue("case " + v.ID)
		return
	}
	v.FirstSeen = now
	v.LastSeen = now
	v.Status = "open"
	v.Observed = true
	c.state.Cases = append(c.state.Cases, v)
	c.enqueue("case " + v.ID)
}

// Events requests a fresh snapshot on every reconnect. There is no cross-ledger
// replay cursor: periodic full program checks cover gaps and deployment resets.
func (p Probe) Events(ctx context.Context, wake chan<- struct{}) error {
	token, err := os.ReadFile(p.TokenFile)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+"/api/v1/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	client := *p.HTTP
	client.Timeout = 0
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("maintenance: event stream unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("maintenance: event stream HTTP %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event: ") {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
	return scanner.Err()
}

func eventLoop(ctx context.Context, p Probe, wake chan<- struct{}) {
	for ctx.Err() == nil {
		_ = p.Events(ctx, wake)
		t := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
