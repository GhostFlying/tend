package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/GhostFlying/tend/internal/protocol"
)

// runLogin gets an owner and an agent token for this installation and
// stores them with the server's address.
func runLogin(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "login")
	server := fs.String("server", "", "the tend server's URL, such as http://tend:8080 (default: the one used before)")
	if pos, err := parse(fs, args); err != nil {
		return err
	} else if len(pos) > 0 {
		return usagef("login takes no arguments")
	}
	cfg, cred, err := e.loadConfig()
	if err != nil && !errors.Is(err, errNotLoggedIn) {
		return err
	}
	if *server != "" {
		u, err := url.Parse(*server)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return usagef("--server must be an http or https URL")
		}
		cfg.Server = *server
	}
	if cfg.Server == "" {
		return usagef("no server yet; pass --server <url>")
	}
	if cred.ClientID == "" {
		cred.ClientID = newClientID()
	}
	c := &client{http: e.HTTP, server: cfg.Server}
	var views [2]protocol.SessionView
	for i, scope := range []protocol.TokenScope{protocol.ScopeOwner, protocol.ScopeAgent} {
		if err := c.do(ctx, "POST", "/api/v1/session", protocol.SessionRequest{ClientID: cred.ClientID, Scope: scope}, &views[i]); err != nil {
			return fmt.Errorf("get the %s token: %w", scope, err)
		}
	}
	cred.Owner, cred.Agent = views[0].Token, views[1].Token
	if err := e.saveConfig(cfg, cred); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "Logged in to %s as %s (client %s); the tokens expire at %s.\n",
		cfg.Server, views[0].Login, cred.ClientID, views[0].ExpiresAt.Local().Format("2006-01-02 15:04"))
	return nil
}
