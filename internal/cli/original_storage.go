package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

// runOriginalStorage manages existing immutable originals. Import deliberately
// uses an uploaded file ID; attachment upload remains the single upload path.
func runOriginalStorage(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("storage originals expects status, verify, import or migrate")
	}
	action := args[0]
	if action != "status" && action != "verify" && action != "import" && action != "migrate" {
		return usagef("storage originals expects status, verify, import or migrate")
	}
	fs := newFlags(e, "storage originals "+action)
	fs.Usage = func() {
		fmt.Fprintln(e.Stderr, "tend storage originals status|verify|import|migrate. Upload complete JSONL files using tend attachment upload before import. Retain the exact input, revision and cursor. After an unknown mutation, read status before deciding whether to continue.")
		fs.PrintDefaults()
	}
	asJSON := fs.Bool("json", false, "print the complete structured result")
	limit := fs.Int("limit", 100, "bounded records/bodies per call (1..1000)")
	after := fs.String("after", "", "opaque verification cursor or import byte cursor")
	revision := fs.Int64("revision", -1, "original storage revision; required for mutation")
	request := fs.String("request-id", "", "transport trace identity; reuse for diagnostics, not a cached result")
	file := fs.String("file-id", "", "complete JSONL original from attachment upload")
	source := fs.String("source-key", "", "stable native source identity for this complete original")
	rescan := fs.Bool("rescan", false, "restart historical scan from the beginning (migrate only)")
	dry := fs.Bool("dry-run", false, "print exact mutation Envelope without writing")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 || *limit < 1 || *limit > 1000 {
		return usagef("unexpected argument or limit outside 1..1000")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	if action == "status" || action == "verify" {
		if *rescan || *dry || *revision != -1 || *request != "" || *file != "" || *source != "" {
			return usagef("read operations do not accept mutation fields")
		}
		path := "/api/v1/storage/originals"
		if action == "verify" {
			query := url.Values{"after": {*after}, "limit": {strconv.Itoa(*limit)}}
			path += "/verify?" + query.Encode()
		} else if *after != "" {
			return usagef("status takes no cursor")
		}
		var result json.RawMessage
		if err = c.do(ctx, http.MethodGet, path, nil, &result); err != nil {
			return err
		}
		if *asJSON {
			return (owner{e: e, json: true}).printJSON(result)
		}
		_, err = fmt.Fprintln(e.Stdout, string(result))
		return err
	}
	if *revision < 1 {
		return usagef("mutation requires the current positive --revision from storage originals status")
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("malformed original request ID")
	}
	operation := protocol.OpMigrateOriginals
	var input any = protocol.OriginalMigration{Limit: *limit, Rescan: *rescan}
	if action == "import" {
		if *rescan {
			return usagef("--rescan is only valid for migrate")
		}
		cursor := int64(0)
		if *after != "" {
			cursor, err = strconv.ParseInt(*after, 10, 64)
			if err != nil || cursor < 0 {
				return usagef("import --after must be a nonnegative byte cursor")
			}
		}
		if *file == "" || *source == "" {
			return usagef("import requires --file-id and --source-key")
		}
		operation = protocol.OpImportOriginals
		input = protocol.OriginalImport{FileID: *file, SourceKey: *source, After: cursor, Limit: *limit}
	} else if *after != "" || *file != "" || *source != "" {
		return usagef("migrate does not accept import or verification fields")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	envelope := protocol.Envelope{RequestID: *request, Operation: operation, Target: protocol.Target{Type: "storage", ID: "originals"}, ExpectedControlRevision: revision, Payload: payload, Reason: "Explicit bounded original storage control"}
	o := owner{e: e, c: c, json: true}
	if *dry {
		return o.printJSON(envelope)
	}
	fmt.Fprintf(e.Stderr, "request %s; revision %d; preserve the original Envelope and cursor for retry\n", *request, *revision)
	result, err := o.sendEnvelope(ctx, envelope)
	if err != nil {
		return err
	}
	if err = o.printJSON(result); err != nil {
		return err
	}
	if result.Error != nil {
		return result.Error
	}
	if result.Operation == nil {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "original storage request has no receipt; query original request %s", *request)
	}
	if result.Operation.Error != nil {
		return result.Operation.Error
	}
	if result.Operation.Status != protocol.OpSucceeded && result.Operation.Status != protocol.OpAccepted {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "original storage request %s is %s", *request, result.Operation.Status)
	}
	return nil
}
