package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runManagerReconciliation(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "task reconcile-manager")
	fs.Bool("json", false, "print the complete operation receipt as JSON (also the default)")
	data := fs.String("data", "", "complete original JSON payload; do not combine with proof flags")
	file := fs.String("file", "", "complete original JSON payload file; - reads stdin")
	request := fs.String("request-id", "", "original request ID for an identical retry")
	revision := fs.Int64("revision", -1, "required original task control revision; never refreshed automatically")
	dry := fs.Bool("dry-run", false, "export the original Envelope without sending")
	var payload protocol.ReconcileManagerRunPayload
	fs.StringVar(&payload.Mode, "mode", "", "read_receipts releases proved-closed transport for a receipt reader; omit to settle waiting")
	fs.StringVar(&payload.RunID, "run", "", "exact original manager Run ID")
	fs.StringVar(&payload.LeaseID, "lease", "", "exact original lease ID")
	fs.StringVar(&payload.CloseRequestID, "close-request", "", "original successful native close request ID")
	fs.StringVar(&payload.CloseResultSHA256, "close-sha256", "", "SHA256 of the original successful close result")
	fs.StringVar(&payload.StoppedRequestID, "stopped-request", "", "original independently accepted native stopped request ID; required for read_receipts")
	fs.StringVar(&payload.StoppedPayloadSHA256, "stopped-sha256", "", "SHA256 of the original stopped payload; required for read_receipts")
	fs.Int64Var(&payload.ExpectedAssignmentEpoch, "assignment-epoch", 0, "required original Issue assignment epoch")
	fs.StringVar(&payload.SourceMessageID, "source-message", "", "optional original owner source message; requires --source-revision")
	sourceRevision := fs.Int64("source-revision", -1, "original source message revision when --source-message is supplied")
	reason := fs.String("reason", "", "payload and Envelope reason with proof flags; Envelope reason only with JSON input")
	fs.Usage = func() {
		fmt.Fprintln(e.Stderr, "usage: tend task reconcile-manager TASK --revision REVISION [proof flags | --data JSON | --file FILE]\nUse --mode read_receipts only with the original close and independent stopped proofs.\nBusiness receipts remain unknown until the original native history is read.\nRetain the complete payload, revision, and request ID; use --dry-run to export the Envelope.")
		fs.PrintDefaults()
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *revision < 0 {
		return usagef("reconcile-manager requires the exact task and its original --revision")
	}
	body, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if len(body) > 0 {
		mixed := ""
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "mode", "run", "lease", "close-request", "close-sha256", "stopped-request", "stopped-sha256", "assignment-epoch", "source-message", "source-revision":
				mixed = f.Name
			}
		})
		if mixed != "" {
			return usagef("complete JSON input cannot be combined with --%s", mixed)
		}
		if json.Unmarshal(body, &payload) != nil {
			return usagef("manager reconciliation payload must be a JSON object")
		}
	} else {
		payload.Reason = *reason
		if *sourceRevision >= 0 {
			payload.ExpectedSourceRevision = sourceRevision
		} else if *sourceRevision != -1 {
			return usagef("source-revision must be nonnegative")
		}
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	if payload.Mode != "" && payload.Mode != "read_receipts" {
		return usagef("mode must be omitted or read_receipts")
	}
	if strings.TrimSpace(payload.RunID) == "" || strings.TrimSpace(payload.LeaseID) == "" || strings.TrimSpace(payload.CloseRequestID) == "" || !managerProofSHA256(payload.CloseResultSHA256) || payload.ExpectedAssignmentEpoch <= 0 || strings.TrimSpace(payload.Reason) == "" {
		return usagef("original run, lease, close-request, close-sha256, assignment-epoch and reason are required")
	}
	if payload.Mode == "read_receipts" && (strings.TrimSpace(payload.StoppedRequestID) == "" || !managerProofSHA256(payload.StoppedPayloadSHA256)) {
		return usagef("read_receipts requires the original stopped-request and stopped-sha256")
	}
	if (payload.SourceMessageID == "") != (payload.ExpectedSourceRevision == nil) || payload.ExpectedSourceRevision != nil && *payload.ExpectedSourceRevision < 0 {
		return usagef("source-message and its nonnegative source-revision must be supplied together")
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("malformed request identity")
	}
	env := protocol.Envelope{RequestID: *request, Operation: protocol.OpReconcileManagerRun, Target: protocol.Target{Type: "task", ID: pos[0]}, ExpectedControlRevision: revision, Payload: body, Reason: *reason}
	if *dry {
		return (owner{e: e, json: true}).printJSON(env)
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	return sendDirectEnvelope(ctx, owner{e: e, c: c, json: true}, env)
}

func managerProofSHA256(raw string) bool {
	decoded, err := hex.DecodeString(raw)
	return err == nil && len(decoded) == sha256.Size
}
