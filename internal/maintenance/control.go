package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (c *Controller) Handler(wake chan<- struct{}) http.Handler {
	mux := http.NewServeMux()
	c.upgradeHandlers(mux)
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /maintenance/status", func(w http.ResponseWriter, _ *http.Request) {
		s := c.Status()
		s.Cases = nil
		s.History = nil
		write(w, s)
	})
	mux.HandleFunc("GET /maintenance/config", func(w http.ResponseWriter, _ *http.Request) { write(w, c.Status().Backend) })
	mux.HandleFunc("GET /maintenance/session", func(w http.ResponseWriter, _ *http.Request) { write(w, c.Status().Active) })
	mux.HandleFunc("GET /maintenance/cases", func(w http.ResponseWriter, r *http.Request) {
		s := c.Status()
		start, limit, err := page(r, len(s.Cases))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		end := min(len(s.Cases), start+limit)
		write(w, map[string]any{"items": s.Cases[start:end], "next": end, "more": end < len(s.Cases)})
	})
	mux.HandleFunc("GET /maintenance/history", func(w http.ResponseWriter, r *http.Request) {
		start, limit, err := page(r, -1)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		items, next, more, err := c.History(r.Context(), int64(start), limit)
		if err != nil {
			controlError(w, http.StatusInternalServerError, protocol.CodeInternal, "history is unavailable")
			return
		}
		write(w, map[string]any{"items": items, "next": next, "more": more})
	})
	mux.HandleFunc("GET /maintenance/show/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, v := range c.Status().Cases {
			if v.ID == r.PathValue("id") {
				write(w, v)
				return
			}
		}
		http.Error(w, "case not found", 404)
	})
	mux.HandleFunc("GET /maintenance/request/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := c.Receipt(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, "request not found", 404)
			return
		}
		write(w, v)
	})
	for _, action := range []string{"check", "audit", "pause", "resume", "defer", "resolve", "config", "reconcile", "review", "upgrade-plan", "upgrade-apply"} {
		mux.HandleFunc("POST /maintenance/"+action, func(w http.ResponseWriter, r *http.Request) {
			var req protocol.MaintenanceRequest
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
			d.DisallowUnknownFields()
			if err := d.Decode(&req); err != nil {
				http.Error(w, "invalid command", 400)
				return
			}
			if err := d.Decode(new(any)); err != io.EOF {
				http.Error(w, "trailing command input", 400)
				return
			}
			v, err := c.Command(r.Context(), action, req)
			if err != nil {
				c.opts.Log.ErrorContext(r.Context(), "maintenance control rejected", "trace_id", req.RequestID, "request_id", req.RequestID, "action", action, "err", err)
				code := protocol.CodeOf(err)
				message := "maintenance control failed; inspect controller status"
				status := http.StatusInternalServerError
				if pe, ok := errors.AsType[*protocol.Error](err); ok {
					message = pe.Message
					status = http.StatusConflict
				}
				controlError(w, status, code, message)
				return
			}
			write(w, v)
			select {
			case wake <- struct{}{}:
			default:
			}
		})
	}
	return mux
}

func controlError(w http.ResponseWriter, status int, code protocol.Code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "error": protocol.Errorf(code, "%s", message).Error()})
}

func page(r *http.Request, size int) (int, int, error) {
	start, limit := 0, 50
	var err error
	if raw := r.URL.Query().Get("after"); raw != "" {
		start, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, err
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, err
		}
	}
	if start < 0 || size >= 0 && start > size || limit < 1 || limit > 100 {
		return 0, 0, errors.New("invalid pagination")
	}
	return start, limit, nil
}

// Run owns only its HTTP listener, event reader and controller connection.
func (c *Controller) Run(ctx context.Context, p Probe) error {
	if info, err := os.Lstat(c.cfg.ControlSocket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("maintenance: control path is not a socket")
		}
		conn, err := net.DialTimeout("unix", c.cfg.ControlSocket, time.Second)
		if err == nil {
			_ = conn.Close()
			return errors.New("maintenance: control socket is already live")
		}
		if err := os.Remove(c.cfg.ControlSocket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	l, err := net.Listen("unix", c.cfg.ControlSocket)
	if err != nil {
		return err
	}
	defer l.Close()
	if err := os.Chmod(c.cfg.ControlSocket, 0600); err != nil {
		return err
	}
	wake := make(chan struct{}, 1)
	events := make(chan struct{}, 1)
	srv := &http.Server{Handler: c.Handler(wake), ReadHeaderTimeout: 5 * time.Second}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { eventLoop(ctx, p, events) })
	serverErr := make(chan error, 1)
	wg.Go(func() { serverErr <- srv.Serve(l) })
	defer func() { cancel(); _ = srv.Close(); wg.Wait() }()
	check := time.NewTicker(time.Duration(c.cfg.CheckSeconds) * time.Second)
	defer check.Stop()
	active := time.NewTicker(5 * time.Second)
	defer active.Stop()
	_ = c.Check(ctx, p)
	var lastEventCheck time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-serverErr:
			return err
		case <-check.C:
			_ = c.Check(ctx, p)
		case <-events:
			if c.opts.Now().Sub(lastEventCheck) >= 15*time.Second {
				_ = c.Check(ctx, p)
				lastEventCheck = c.opts.Now()
			}
		case <-wake:
		case <-active.C:
		}
		if c.Status().CheckRequested {
			_ = c.Check(ctx, p)
		}
		stepErr := c.Step(ctx)
		message := ""
		if stepErr != nil {
			message = stepErr.Error()
		}
		c.mu.Lock()
		if c.fault == nil && !c.frozen() && c.state.NativeError != message {
			c.state.NativeError = message
			_ = c.save(ctx)
		}
		c.mu.Unlock()
	}
}
