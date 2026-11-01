package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runLocalRepository(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("repo local list|bind --socket <path>")
	}
	action := args[0]
	if action != "list" && action != "status" && action != "bind" {
		return usagef("repo local expects list, status or bind")
	}
	fs := newFlags(e, "repo local "+action)
	socket := fs.String("socket", "", "runner-private control socket")
	data := fs.String("data", "", "complete local binding JSON (repo, remote_url, orca)")
	file := fs.String("file", "", "binding JSON file; - reads stdin")
	repo := fs.String("repo", "", "registered repository name")
	remote := fs.String("remote", "", "registered remote URL")
	orca := fs.String("orca", "", "existing Orca repository selector")
	request := fs.String("request-id", "", "original request ID for identical retry")
	revision := fs.Int64("revision", -1, "original local repository control revision")
	dry := fs.Bool("dry-run", false, "print original local request without mutation")
	asJSON := fs.Bool("json", true, "print complete structured observation")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 || !filepath.IsAbs(*socket) {
		return usagef("local repository operations require --socket and no positional arguments")
	}
	if *revision < -1 {
		return usagef("revision must be nonnegative")
	}
	var status protocol.LocalRepositoryStatus
	if action != "bind" {
		if *data != "" || *file != "" || *request != "" || *revision != -1 || *dry || *repo != "" || *remote != "" || *orca != "" {
			return usagef("local repository listing takes no mutation input")
		}
		if err := localUpgradeRequest(ctx, *socket, http.MethodGet, "/repositories", nil, &status); err != nil {
			return err
		}
		raw, _ := json.Marshal(status)
		return printCapabilityValue(e, raw, *asJSON)
	}
	raw, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		if *repo == "" || *remote == "" {
			return usagef("bind requires --repo/--remote or the complete binding JSON")
		}
		raw, err = json.Marshal(protocol.LocalRepositoryBinding{Repo: *repo, RemoteURL: *remote, Orca: *orca})
		if err != nil {
			return err
		}
	} else if *repo != "" || *remote != "" || *orca != "" {
		return usagef("choose complete binding JSON or binding flags")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return usagef("binding must be a JSON object")
	}
	if *request != "" {
		if !protocol.ValidID(protocol.PrefixRequest, *request) {
			return usagef("malformed request identity")
		}
		if *revision < 0 {
			return usagef("retry requires original --revision and binding")
		}
	}
	if *revision < 0 {
		if err := localUpgradeRequest(ctx, *socket, http.MethodGet, "/repositories", nil, &status); err != nil {
			return err
		}
		*revision = status.Revision
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	input := map[string]any{"request_id": *request, "expected_control_revision": *revision, "binding": json.RawMessage(raw)}
	if *dry {
		return (owner{e: e, json: true}).printJSON(map[string]any{"method": "POST", "path": "/repositories/bind", "request": input})
	}
	fmt.Fprintf(e.Stderr, "Local repository request %s; revision %d; preserve the complete binding for retry\n", *request, *revision)
	if err := localUpgradeRequest(ctx, *socket, http.MethodPost, "/repositories/bind", input, &status); err != nil {
		return err
	}
	raw, _ = json.Marshal(status)
	return printCapabilityValue(e, raw, *asJSON)
}
