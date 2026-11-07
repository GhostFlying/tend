package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runIssueDeploymentPreparation(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "issue deployment-preparation")
	fs.Usage = func() {
		fmt.Fprint(e.Stderr, commandUsage([]string{"issue", "deployment-preparation"}))
		fs.PrintDefaults()
	}
	asJSON := fs.Bool("json", true, "print the complete deployment preparation response as JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" {
		return usagef("issue deployment-preparation requires one Issue ID")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	var value json.RawMessage
	if err := c.do(ctx, "GET", "/api/v1/issues/"+url.PathEscape(pos[0])+"/deployment-preparation", nil, &value); err != nil {
		return err
	}
	return printCapabilityValue(e, value, *asJSON)
}
