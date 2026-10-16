package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// Read limits.
const (
	// taskPageSize is the length of a page of finished tasks.
	taskPageSize = 50
	// logPageSize is the most run logs a page holds.
	logPageSize = 500
	// maxLogWait bounds how long a run log read waits for new logs.
	maxLogWait = 25 * time.Second
)

// read runs fn in a read transaction for an owner, mapping a missing row
// to not_found, and writes what it returns.
func (s *Server) read(w http.ResponseWriter, r *http.Request, fn func(p protocol.Principal, q store.Querier) (any, error)) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var out any
	err = s.store.Read(r.Context(), func(tx *store.ReadTx) error {
		var err error
		out, err = fn(p, tx.Q())
		return err
	})
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func notFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return protocol.Errorf(protocol.CodeNotFound, "%s not found", strings.TrimSuffix(err.Error(), ": "+store.ErrNotFound.Error()))
	}
	return err
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.Snapshot(r.Context(), q)
	})
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.Task(r.Context(), q, r.PathValue("id"))
	})
}

// tasks lists the unfinished tasks, or with finished=1 a page of the
// finished ones, newest first.
func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("finished") != "1" {
		s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
			list, err := views.Tasks(r.Context(), q, false)
			if list == nil {
				list = []protocol.TaskSummary{}
			}
			return protocol.TaskPage{Tasks: list}, err
		})
		return
	}
	var updated int64
	var id string
	if before := query.Get("before"); before != "" {
		ms, rest, ok := strings.Cut(before, ":")
		n, err := strconv.ParseInt(ms, 10, 64)
		if !ok || err != nil || n <= 0 || rest == "" {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "before must be a cursor from a previous page"))
			return
		}
		updated, id = n, rest
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		rows, err := store.ListFinishedTasks(r.Context(), q, updated, id, taskPageSize)
		if err != nil {
			return nil, err
		}
		page := protocol.TaskPage{Tasks: make([]protocol.TaskSummary, 0, len(rows))}
		for _, row := range rows {
			page.Tasks = append(page.Tasks, views.Summary(row))
		}
		if len(rows) == taskPageSize {
			last := rows[len(rows)-1]
			page.Next = fmt.Sprintf("%d:%s", last.UpdatedAt, last.TaskID)
		}
		return page, nil
	})
}

// request reads the outcome of one of the owner's requests.
func (s *Server) request(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(p protocol.Principal, q store.Querier) (any, error) {
		return views.Request(r.Context(), q, p.DedupDomain(), r.PathValue("id"))
	})
}

// runLogs reads a page of a run's logs after a sequence number. With wait,
// a read that finds nothing new waits up to that many seconds, at most
// maxLogWait, for logs to arrive or the run to end, unless it has ended.
func (s *Server) runLogs(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	query := r.URL.Query()
	after, err := intParam(query.Get("after"))
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "after must be a sequence number"))
		return
	}
	wait, err := intParam(query.Get("wait"))
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "wait must be a number of seconds"))
		return
	}
	var deadline <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(min(time.Duration(wait)*time.Second, maxLogWait))
		defer t.Stop()
		deadline = t.C
	}
	ctx := r.Context()
	for {
		logged, changed := s.app.RunLogs().Wait(), s.app.Changes().Wait()
		var page protocol.RunLogPage
		err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			page, err = views.RunLogs(ctx, tx.Q(), r.PathValue("id"), after, logPageSize)
			return err
		})
		if err != nil {
			s.writeError(w, r, notFound(err))
			return
		}
		if len(page.Logs) > 0 || deadline == nil || ended(page.Observed) {
			writeJSON(w, http.StatusOK, page)
			return
		}
		select {
		case <-logged:
		case <-changed:
		case <-deadline:
			writeJSON(w, http.StatusOK, page)
			return
		case <-ctx.Done():
			return
		}
	}
}

// ended reports whether a run in state o will record no more logs. An
// interrupted run may still resume.
func ended(o protocol.RunObserved) bool {
	return o == protocol.RunFinished || o == protocol.RunFailed
}

// intParam parses an optional non-negative integer.
func intParam(v string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("not a non-negative integer: %q", v)
	}
	return n, nil
}
