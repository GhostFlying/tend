package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runBoundedRunnerAdmissionRelease(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "host release-bounded-admission")
	data := fs.String("data", "", "complete typed BoundedRunnerAdmissionRelease JSON")
	file := fs.String("file", "", "complete typed release file; - reads stdin")
	request := fs.String("request-id", "", "original explicit release request; must match the payload")
	revision := fs.Int64("revision", -1, "original host control CAS; must match the payload")
	dry := fs.Bool("dry-run", false, "export the original owner Envelope without sending")
	fs.Bool("json", false, "print the complete operation receipt (also the default)")
	fs.Usage = func() {
		fmt.Fprint(e.Stderr, commandUsage([]string{"host", "release-bounded-admission"}))
		fs.PrintDefaults()
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *revision < -1 {
		return usagef("release-bounded-admission requires the exact host and original control revision")
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	raw, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	var p protocol.BoundedRunnerAdmissionRelease
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return usagef("invalid runner admission release: %v", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return usagef("trailing runner admission release data")
	}
	if p.HostID != pos[0] || !protocol.ValidID(protocol.PrefixRequest, p.RequestID) || p.ExpectedControlRevision < 0 || p.RunnerGeneration <= 0 || strings.TrimSpace(p.RecoveryAuthorizationID) == "" || strings.TrimSpace(p.Reason) == "" {
		return usagef("runner admission release requires this host, original force authorization, successor generation, explicit request/CAS and reason")
	}
	if provided["request-id"] && *request != p.RequestID || provided["revision"] && *revision != p.ExpectedControlRevision {
		return usagef("runner admission release flags conflict with the original request or host CAS")
	}
	*request, *revision = p.RequestID, p.ExpectedControlRevision
	env := protocol.Envelope{
		Operation: protocol.OpReleaseBoundedRunnerAdmission, RequestID: *request,
		Target: protocol.Target{Type: "host", ID: p.HostID}, ExpectedControlRevision: revision,
		Payload: raw, Reason: p.Reason,
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

func boundedRunnerAdmissionReleaseExample() string {
	p := protocol.BoundedRunnerAdmissionRelease{
		RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEZ", HostID: "sample", RecoveryAuthorizationID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY",
		Successor:        protocol.UpgradeBinding{UpgradeID: "bounded-window", InstanceID: "sample", ProcessInstanceID: "successor-runner", ReleaseID: "successor-release", ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: strings.Repeat("c", 64)},
		RunnerGeneration: 2, Journal: protocol.BoundedDataIdentity{Device: 1, Inode: 2, Incarnation: "original-journal", Version: 17},
		ExpectedControlRevision: 2, Reason: "Admit new work after exact successor reconciliation; retain old Run quarantine",
	}
	raw, _ := json.Marshal(p)
	return string(raw)
}
