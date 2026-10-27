package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/GhostFlying/tend/internal/protocol"
)

// runLogin gets an owner token for this installation and
// stores it with the server's address.
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
	var view protocol.SessionView
	if err := c.do(ctx, "POST", "/api/v1/session", protocol.SessionRequest{ClientID: cred.ClientID, Scope: protocol.ScopeOwner}, &view); err != nil {
		return fmt.Errorf("get the owner token: %w", err)
	}
	if view.DeploymentID == "" {
		return fmt.Errorf("server deployment identity missing")
	}
	cred.DeploymentID = view.DeploymentID
	cred.Owner = view.Token
	if err := e.saveConfig(cfg, cred); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "Logged in to %s as %s (client %s); the token expires at %s.\n",
		cfg.Server, view.Login, cred.ClientID, view.ExpiresAt.Local().Format("2006-01-02 15:04"))
	return nil
}
