// Command tsnet is a phase A spike that joins the tailnet with an embedded
// tsnet node, serves HTTP and, when available, HTTPS, and answers each
// request with the caller identity reported by WhoIs. See README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

func main() {
	hostname := flag.String("hostname", envOr("TEND_TS_HOSTNAME", "tend-dev"), "tailnet hostname")
	stateDir := flag.String("state", filepath.Join(envOr("TEND_DATA_DIR", os.TempDir()), "tsnet-spike"), "tsnet state directory")
	ephemeral := flag.Bool("ephemeral", true, "register as an ephemeral node")
	flag.Parse()

	srv := &tsnet.Server{
		Hostname:  *hostname,
		Dir:       *stateDir,
		AuthKey:   os.Getenv("TS_AUTHKEY"),
		Ephemeral: *ephemeral,
		UserLogf:  log.Printf,
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	upCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	status, err := srv.Up(upCtx)
	cancel()
	if err != nil {
		log.Fatalf("tsnet up: %v", err)
	}
	lc, err := srv.LocalClient()
	if err != nil {
		log.Fatalf("local client: %v", err)
	}
	log.Printf("up: dns=%s ips=%v certDomains=%v", status.Self.DNSName, status.TailscaleIPs, status.CertDomains)

	handler := whoisHandler(lc)
	ln, err := srv.Listen("tcp", ":80")
	if err != nil {
		log.Fatalf("listen :80: %v", err)
	}
	go serve(ln, handler, "http")

	if len(status.CertDomains) > 0 {
		tlsLn, err := srv.ListenTLS("tcp", ":443")
		if err != nil {
			log.Printf("listen tls: %v", err)
		} else {
			go serve(tlsLn, handler, "https")
		}
	} else {
		log.Printf("no cert domains: HTTPS certificates are not enabled for this tailnet")
	}

	<-ctx.Done()
}

func whoisHandler(lc *local.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, err := lc.WhoIs(r.Context(), r.RemoteAddr)
		resp := map[string]any{"remoteAddr": r.RemoteAddr, "tls": r.TLS != nil}
		if err != nil {
			resp["whoisError"] = err.Error()
		} else {
			resp["user"] = who.UserProfile.LoginName
			resp["node"] = strings.TrimSuffix(who.Node.Name, ".")
			resp["tags"] = who.Node.Tags
			resp["nodeKey"] = who.Node.Key.ShortString()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func serve(ln net.Listener, h http.Handler, name string) {
	err := (&http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}).Serve(ln)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("%s server: %v", name, err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

