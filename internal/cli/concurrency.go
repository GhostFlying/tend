package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runConcurrency(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("agents concurrency expects list|show|create|update|bind|unbind|delete|history")
	}
	action := args[0]
	fs := newFlags(e, "agents concurrency "+action)
	fs.Bool("json", true, "print complete structured result, including all decision evidence")
	name := fs.String("name", "", "group display name")
	minimum := fs.Int64("min", -1, "minimum concurrent executions, at least 1")
	initial := fs.Int64("initial", -1, "initial budget; changing this does not reset the learned value")
	maximum := fs.Int64("max", -1, "maximum concurrent executions")
	group := fs.String("group", "", "group ID filter")
	environment := fs.String("environment", "", "execution environment ID (required for bind/unbind)")
	runtime := fs.String("runtime", "", "runtime ID (required for bind/unbind)")
	issue := fs.String("issue", "", "filter history by Issue ID")
	session := fs.String("session", "", "filter history by Tend or native session ID")
	cause := fs.String("cause", "", "history reason: initial, configuration, bind, unbind, congestion, minimum_cooldown, stable_demand")
	search := fs.String("search", "", "case-insensitive name/ID or history search")
	after := fs.String("after", "", "exclusive cursor from preceding page")
	limit := fs.Int("limit", 50, "page size, 1..100")
	since := fs.Int64("since", 0, "inclusive UTC milliseconds, restricted to retained seven days")
	until := fs.Int64("until", 0, "inclusive UTC milliseconds; default current time")
	revision := fs.Int64("revision", -1, "expected configuration revision, read automatically unless replaying")
	request := fs.String("request-id", "", "original request ID; replay requires original revision and input")
	reason := fs.String("reason", "", "owner reason retained with original request")
	dry := fs.Bool("dry-run", false, "print the exact replayable Envelope without writing")
	data := fs.String("data", "", "complete mutation payload JSON")
	file := fs.String("file", "", "complete payload JSON file; - reads stdin")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	allowed := "json "
	switch action {
	case "list":
		allowed += "group search environment runtime after limit"
	case "show":
	case "history":
		allowed += "group search environment runtime issue session cause since until after limit"
	case "create", "update":
		allowed += "name min initial max data file request-id revision reason dry-run"
	case "bind", "unbind":
		allowed += "environment runtime data file request-id revision reason dry-run"
	case "delete":
		allowed += "data file request-id revision reason dry-run"
	}
	valid := map[string]bool{}
	for _, name := range strings.Fields(allowed) {
		valid[name] = true
	}
	invalid := ""
	fs.Visit(func(f *flag.Flag) {
		if !valid[f.Name] {
			invalid = f.Name
		}
	})
	if invalid != "" {
		return usagef("--%s does not apply to %s", invalid, action)
	}
	if *limit < 1 || *limit > 100 || *since < 0 || *until < 0 {
		return usagef("invalid page size or time range")
	}
	read := action == "list" || action == "show" || action == "history"
	if !read && action != "create" && action != "update" && action != "bind" && action != "unbind" && action != "delete" {
		return usagef("unknown concurrency command")
	}
	if (action == "list" || action == "history") && len(pos) != 0 || action != "list" && action != "history" && len(pos) != 1 {
		return usagef("list/history accept filters; other commands require one group ID")
	}
	if read && (*data != "" || *file != "" || *request != "" || *revision != -1 || *dry || *name != "" || *minimum != -1 || *initial != -1 || *maximum != -1) {
		return usagef("read commands do not accept mutation input")
	}
	if action == "update" && *request != "" && *data == "" && *file == "" && (*name == "" || *minimum < 0 || *initial < 0 || *maximum < 0) {
		return usagef("retry update requires the original complete --data/--file or all four parameter flags")
	}
	if !read && *request != "" && *revision < 0 {
		return usagef("retry requires the original --revision with --request-id")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	if read {
		path := "/api/v1/concurrency-groups"
		q := url.Values{"group_id": {*group}, "environment_id": {*environment}, "runtime_id": {*runtime}, "issue_id": {*issue}, "session_id": {*session}, "reason": {*cause}, "search": {*search}, "after": {*after}, "limit": {strconv.Itoa(*limit)}, "since": {strconv.FormatInt(*since, 10)}, "until": {strconv.FormatInt(*until, 10)}}
		if action == "show" {
			path += "/" + url.PathEscape(pos[0])
		} else if action == "history" {
			path = "/api/v1/concurrency-history"
		}
		var result json.RawMessage
		if err := c.do(ctx, "GET", path+"?"+q.Encode(), nil, &result); err != nil {
			return err
		}
		return json.NewEncoder(e.Stdout).Encode(result)
	}
	input, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	current := protocol.ConcurrencyGroup{}
	if action != "create" && (*revision < 0 || action == "update" && len(input) == 0 && (*name == "" || *minimum < 0 || *initial < 0 || *maximum < 0)) {
		if err := c.do(ctx, "GET", "/api/v1/concurrency-groups/"+url.PathEscape(pos[0]), nil, &current); err != nil {
			return err
		}
	}
	if *revision < 0 {
		*revision = current.Revision
	}
	p := protocol.ConfigureConcurrencyPayload{EnvironmentID: *environment, RuntimeID: *runtime}
	if action == "create" || action == "update" {
		p.Name, p.Min, p.Initial, p.Max = current.Name, current.Min, current.Initial, current.Max
	}
	if *name != "" {
		p.Name = *name
	}
	if *minimum != -1 {
		p.Min = *minimum
	}
	if *initial != -1 {
		p.Initial = *initial
	}
	if *maximum != -1 {
		p.Max = *maximum
	}
	if len(input) == 0 {
		input, err = json.Marshal(p)
		if err != nil {
			return err
		}
	} else if err := json.Unmarshal(input, &p); err != nil {
		return usagef("invalid payload JSON")
	}
	if (action == "create" || action == "update") && (p.Name == "" || p.Min < 1 || p.Min > p.Initial || p.Initial > p.Max) {
		return usagef("name and 1 <= min <= initial <= max required")
	}
	if (action == "bind" || action == "unbind") && (p.EnvironmentID == "" || p.RuntimeID == "") {
		return usagef("bind/unbind require --environment and --runtime, or a complete payload")
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	env := protocol.Envelope{RequestID: *request, Operation: "concurrency." + action, Target: protocol.Target{Type: "concurrency", ID: pos[0]}, ExpectedControlRevision: revision, Payload: input, Reason: *reason}
	if *dry {
		return json.NewEncoder(e.Stdout).Encode(env)
	}
	fmt.Fprintf(e.Stderr, "Concurrency request %s; revision %d\n", *request, *revision)
	var response protocol.APIResponse
	err = c.do(ctx, "POST", "/api/v1/ops", env, &response)
	if isTransport(err) {
		err = c.do(ctx, "POST", "/api/v1/ops", env, &response)
	}
	if err != nil {
		return err
	}
	if response.Operation == nil {
		return fmt.Errorf("no receipt; retain request %s, revision %d and exact payload for request replay", *request, *revision)
	}
	if response.Operation.Error != nil {
		return response.Operation.Error
	}
	if response.Operation.Status != protocol.OpSucceeded {
		return errors.New("concurrency operation pending; retain original request and input")
	}
	return json.NewEncoder(e.Stdout).Encode(response.Operation)
}
