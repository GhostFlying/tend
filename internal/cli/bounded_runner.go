package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

const boundedRunnerOwnerOperation = protocol.OpAuthorizeBoundedRunnerMaintenance

func runBoundedRunnerOwner(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "host authorize-bounded-maintenance")
	data := fs.String("data", "", "complete typed RunnerBoundedMaintenancePlan JSON")
	file := fs.String("file", "", "complete typed plan file; - reads stdin")
	request := fs.String("request-id", "", "original authorization request; must match the typed plan")
	revision := fs.Int64("revision", -1, "original host control CAS; must match the typed plan")
	dry := fs.Bool("dry-run", false, "export the original owner Envelope without sending")
	fs.Bool("json", false, "print the complete operation receipt (also the default)")
	fs.Usage = func() {
		fmt.Fprint(e.Stderr, commandUsage([]string{"host", "authorize-bounded-maintenance"}))
		fs.PrintDefaults()
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *revision < -1 {
		return usagef("authorize-bounded-maintenance requires an exact host and original control revision")
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	raw, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	var p protocol.RunnerBoundedMaintenancePlan
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return usagef("invalid runner bounded plan: %v", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return usagef("trailing runner bounded plan data")
	}
	if p.HostID != pos[0] || strings.TrimSpace(p.NodeSelector) == "" || strings.TrimSpace(p.RunnerInstanceID) == "" || p.RunnerGeneration <= 0 || p.ExpectedControlRevision < 0 || !filepath.IsAbs(p.ReleaseDir) {
		return usagef("runner bounded plan must bind this host, node, original runner and host control revision")
	}
	auth := p.Authorization
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if !protocol.ValidID(protocol.PrefixRequest, auth.RequestID) || auth.Role != "runner" || strings.TrimSpace(auth.Reason) == "" || auth.ExpiresAtMillis <= 0 || !boundedGenerationPresent(fields["authorization"]) || auth.ExpectedPhase == "" || !boundedSHA256(auth.PlanSHA256) {
		return usagef("runner bounded plan requires a complete independent authorization")
	}
	if auth.Source != p.Source || auth.Target != p.Target || auth.DataIdentity == nil || *auth.DataIdentity != p.Journal {
		return usagef("runner authorization source, target and data identity must match the complete plan")
	}
	digest, err := protocol.RunnerBoundedMaintenancePlanSHA256(p)
	if err != nil {
		return err
	}
	if digest != auth.PlanSHA256 {
		return usagef("runner authorization plan digest does not match the original source plan")
	}
	if provided["request-id"] && *request != auth.RequestID || provided["revision"] && *revision != p.ExpectedControlRevision {
		return usagef("runner authorization flags conflict with the original request or host CAS")
	}
	*request, *revision = auth.RequestID, p.ExpectedControlRevision
	env := protocol.Envelope{
		Operation: boundedRunnerOwnerOperation, RequestID: *request,
		Target: protocol.Target{Type: "host", ID: p.HostID}, ExpectedControlRevision: revision,
		Payload: raw, Reason: auth.Reason,
	}
	if *dry {
		return (owner{e: e, json: true}).printJSON(env)
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	return sendDirectEnvelope(ctx, owner{e: e, c: c, json: true}, env)
}

func boundedRunnerPlanExample() string {
	plan := protocol.RunnerBoundedMaintenancePlan{
		HostID: "sample", NodeSelector: "original-node", RunnerInstanceID: "original-runner", RunnerGeneration: 1, ReleaseDir: filepath.Join(os.TempDir(), "reviewed-release"), ExpectedControlRevision: 2,
		Source:  protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: "original-launch", SHA256: strings.Repeat("a", 64)},
		Journal: protocol.BoundedDataIdentity{Device: 1, Inode: 2, Incarnation: "original-journal", Version: 15},
		Target:  protocol.UpgradeBinding{UpgradeID: "bounded-window", InstanceID: "sample", ProcessInstanceID: "successor-runner", ReleaseID: "successor-release", ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: strings.Repeat("c", 64)},
	}
	plan.Authorization = protocol.BoundedMaintenanceAuthorization{
		RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY", ExpectedGeneration: 17, ExpectedPhase: "prepared", PlanSHA256: strings.Repeat("d", 64),
		Role: "runner", Source: plan.Source, Target: plan.Target, DataIdentity: &plan.Journal,
		Reason: "Retain original unknown native work", ExpiresAtMillis: 9999999999999,
		Timeouts: protocol.DefaultMaintenanceTimeoutPolicy(),
	}
	plan.Authorization.PlanSHA256, _ = protocol.RunnerBoundedMaintenancePlanSHA256(plan)
	raw, _ := json.Marshal(plan)
	return string(raw)
}
