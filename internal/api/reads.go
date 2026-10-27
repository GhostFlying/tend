package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// Read limits.
const (
	// taskPageSize is the length of a paginated task listing.
	taskPageSize = 50
	// logPageSize is the most run logs a page holds.
	logPageSize = 500
	// maxLogWait bounds how long a run log read waits for new logs.
	maxLogWait = 25 * time.Second
	// conversationPageSize is the default and the most conversation
	// items a page holds.
	conversationPageSize = 100
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

// conversation reads the conversation in pages (D69): ?before=<seq> pages
// back, ?after=<seq> reads what came later, and neither reads the latest.
func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	before, err := intParam(query.Get("before"))
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "before must be a sequence number"))
		return
	}
	after, err := intParam(query.Get("after"))
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "after must be a sequence number"))
		return
	}
	limit, err := intParam(query.Get("limit"))
	if err != nil || before > 0 && after > 0 {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "give a limit as a number and at most one of before and after"))
		return
	}
	if limit == 0 || limit > conversationPageSize {
		limit = conversationPageSize
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.Conversation(r.Context(), q, before, after, int(limit), r.URL.Query().Get("conversation_id"))
	})
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		queue, err := app.Queue(r.Context(), q, s.app.QueueOrder())
		if err != nil {
			return nil, err
		}
		return views.Snapshot(r.Context(), q, queue, r.URL.Query().Get("project"))
	})
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.Task(r.Context(), q, r.PathValue("id"))
	})
}

// tasks lists unfinished tasks, finished=1 pages finished tasks, and all=1
// pages every lifecycle with optional project and lifecycle filters.
func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	all := query.Get("all") == "1"
	pageSize := taskPageSize
	if all && query.Has("limit") {
		limit, err := strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > taskPageSize {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 50"))
			return
		}
		pageSize = limit
	}
	filter := store.TaskFilter{Project: query.Get("project")}
	if all && query.Has("lifecycle") {
		for _, value := range strings.Split(query.Get("lifecycle"), ",") {
			state := protocol.TaskLifecycle(value)
			if !state.Valid() {
				s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "lifecycle must name known task stages"))
				return
			}
			filter.Lifecycles = append(filter.Lifecycles, state)
		}
	}
	if order := query.Get("sort"); order != "" {
		if order != "completed_at" || !all || len(filter.Lifecycles) != 1 || filter.Lifecycles[0] != protocol.TaskCompleted {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "sort=completed_at requires all=1 and lifecycle=completed"))
			return
		}
		filter.CompletedOrder = true
	}
	if !all && query.Get("finished") != "1" {
		s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
			list, err := views.Tasks(r.Context(), q, false, "")
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
		if !ok || err != nil || (n < 0 || n == 0 && !filter.CompletedOrder) || rest == "" {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "before must be a cursor from a previous page"))
			return
		}
		updated, id = n, rest
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		var rows []store.TaskRow
		var err error
		if all {
			rows, err = store.ListTaskPage(r.Context(), q, filter, updated, id, pageSize+1)
		} else {
			rows, err = store.ListFinishedTasks(r.Context(), q, updated, id, taskPageSize)
		}
		if err != nil {
			return nil, err
		}
		more := len(rows) == taskPageSize && !all
		if all && len(rows) > pageSize {
			more = true
			rows = rows[:pageSize]
		}
		summaries, err := views.SummariesWithExecution(r.Context(), q, rows)
		if err != nil {
			return nil, err
		}
		page := protocol.TaskPage{Tasks: summaries}
		if more {
			last := rows[len(rows)-1]
			cursorTime := last.UpdatedAt
			if filter.CompletedOrder {
				cursorTime = last.CompletedAt
			}
			page.Next = fmt.Sprintf("%d:%s", cursorTime, last.TaskID)
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

// repos reads all registrations, without project filtering or runner secrets.
func (s *Server) repos(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		repos, err := views.RegisteredRepos(r.Context(), q)
		return protocol.RepoRegistry{Repos: repos}, err
	})
}
