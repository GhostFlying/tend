package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runUpgrade(ctx context.Context, e *Env, args []string) error {
	action := "start"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		args = args[1:]
	}
	if action != "start" && action != "status" && action != "retry" && action != "cancel" {
		return usagef("upgrade expects start, status, retry or cancel")
	}
	fs := newFlags(e, "upgrade "+action)
	configPlan := fs.String("runner-configurations", "", "private JSON metadata for a single runner configuration maintenance")
	bundle := fs.String("release", "", "verified local release directory")
	upgradeID := fs.String("upgrade-id", "", "stable upgrade batch ID")
	requestID := fs.String("request-id", "", "reuse this ID only when retrying the identical request")
	revision := fs.Int64("revision", 0, "current upgrade control revision")
	socket := fs.String("socket", e.Getenv("TEND_UPGRADE_SOCKET"), "server local upgrade control socket")
	asJSON := fs.Bool("json", false, "print the release state as JSON")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *configPlan != "" && action != "start" {
		return usagef("runner-configurations is only valid for start")
	}
	if len(positional) > 0 {
		return usagef("upgrade takes no positional arguments")
	}
	if *socket == "" {
		*socket = filepath.Join(e.Home, ".local/share/tend/server/upgrades.sock")
	}
	if !filepath.IsAbs(*socket) {
		return usagef("upgrade --socket must be an absolute path")
	}
	var input any
	method := http.MethodPost
	path := "/upgrades/" + action
	switch action {
	case "start":
		if *bundle == "" || *revision != 0 {
			return usagef("upgrade start requires --release and takes no --revision")
		}
		absolute, err := filepath.Abs(*bundle)
		if err != nil {
			return err
		}
		if *requestID == "" {
			*requestID = e.IDs.New(protocol.PrefixRequest)
		}
		if *upgradeID == "" {
			*upgradeID = *requestID
		}
		var configurations map[string]protocol.RunnerConfiguration
		if *configPlan != "" {
			raw, err := os.ReadFile(*configPlan)
			if err != nil {
				return err
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&configurations); err != nil {
				return usagef("invalid runner configuration metadata")
			}
			if decoder.Decode(new(any)) != io.EOF || len(configurations) != 1 {
				return usagef("configuration metadata must select exactly one host")
			}
		}
		input = protocol.UpgradeStartRequest{RequestID: *requestID, UpgradeID: *upgradeID, Release: absolute, RunnerConfigurations: configurations}
	case "status":
		if *bundle != "" || *upgradeID != "" || *requestID != "" || *revision != 0 {
			return usagef("upgrade status takes no release, ID or revision")
		}
		method = http.MethodGet
	case "retry", "cancel":
		if *bundle != "" || *upgradeID == "" || *revision < 1 {
			return usagef("upgrade retry/cancel requires --upgrade-id and --revision")
		}
		if *requestID == "" {
			*requestID = e.IDs.New(protocol.PrefixRequest)
		}
		input = protocol.UpgradeChangeRequest{RequestID: *requestID, UpgradeID: *upgradeID, ExpectedControlRevision: *revision}
	}
	if *requestID != "" && !protocol.ValidID(protocol.PrefixRequest, *requestID) {
		return usagef("upgrade --request-id is malformed")
	}
	if action != "status" {
		fmt.Fprintf(e.Stderr, "Upgrade %s; request %s\n", *upgradeID, *requestID)
	}
	var view protocol.UpgradeView
	if err := localUpgradeRequest(ctx, *socket, method, path, input, &view); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(e.Stdout).Encode(view)
	}
	if view.UpgradeID == "" {
		_, err = fmt.Fprintln(e.Stdout, "No release batch.")
		return err
	}
	fmt.Fprintf(e.Stdout, "Upgrade %s: %s / %s (revision %d)\n", view.UpgradeID, view.Status, view.Phase, view.Revision)
	if view.Reason != "" {
		fmt.Fprintln(e.Stdout, view.Reason)
	}
	for _, h := range view.Hosts {
		fmt.Fprintf(e.Stdout, "  %s: %s / %s; remaining runs %d\n", h.HostID, h.Status, h.Phase, h.RemainingRuns)
	}
	return nil
}
func localUpgradeRequest(ctx context.Context, socket, method, path string, input, out any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("upgrade control redirects are forbidden")
	}}
	request, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("local upgrade service unavailable; preserve the request ID and inspect status before retrying")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&failure); err == nil && failure.Error != "" {
			return errors.New(failure.Error)
		}
		return fmt.Errorf("local upgrade service returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(out)
}
