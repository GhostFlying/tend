package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestTaskBoardPagesEveryLifecycleBeyondSnapshotLimit(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	e.configure()
	states := []protocol.TaskLifecycle{protocol.TaskDraft, protocol.TaskQueued, protocol.TaskRunning,
		protocol.TaskAwaitingAcceptance, protocol.TaskChangesRequired, protocol.TaskCompleted, protocol.TaskCancelled}
	all := map[string]protocol.TaskLifecycle{}
	projects := map[string]string{}
	for i := range 231 {
		id := e.task(fmt.Sprintf("board task %d", i))
		state, project := states[i%len(states)], "alpha"
		if i%3 == 0 {
			project = "beta"
		}
		if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Exec(t.Context(), "UPDATE tasks SET lifecycle = ?, project = ? WHERE task_id = ?", state, project, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		all[id], projects[id] = state, project
		if i%5 == 0 {
			e.clock.Advance(time.Millisecond)
		}
	}
	for _, tc := range []struct {
		name, project, lifecycle string
	}{
		{name: "all"},
		{name: "project", project: "alpha"},
		{name: "waiting stages", lifecycle: "draft,queued"},
		{name: "completed", lifecycle: "completed"},
		{name: "combined", project: "beta", lifecycle: "completed,cancelled"},
		{name: "empty project", project: "absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := map[string]bool{}
			for id, state := range all {
				if (tc.project == "" || projects[id] == tc.project) &&
					(tc.lifecycle == "" || strings.Contains(","+tc.lifecycle+",", ","+string(state)+",")) {
					want[id] = true
				}
			}
			query := url.Values{"all": {"1"}}
			if tc.project != "" {
				query.Set("project", tc.project)
			}
			if tc.lifecycle != "" {
				query.Set("lifecycle", tc.lifecycle)
			}
			seen, cursors := map[string]bool{}, map[string]bool{}
			for {
				var page protocol.TaskPage
				if st := e.do("GET", "/api/v1/tasks?"+query.Encode(), tok, nil, &page); st != http.StatusOK {
					t.Fatalf("list = %d", st)
				}
				if len(page.Tasks) > taskPageSize {
					t.Fatalf("page contains %d tasks", len(page.Tasks))
				}
				for _, task := range page.Tasks {
					if !want[task.TaskID] || seen[task.TaskID] {
						t.Fatalf("unexpected or duplicate task %s", task.TaskID)
					}
					seen[task.TaskID] = true
				}
				if page.Next == "" {
					break
				}
				if cursors[page.Next] {
					t.Fatal("cursor did not advance")
				}
				cursors[page.Next] = true
				query.Set("before", page.Next)
			}
			if len(seen) != len(want) {
				t.Fatalf("listed %d tasks, want %d", len(seen), len(want))
			}
		})
	}
	var recent protocol.TaskPage
	if st := e.do("GET", "/api/v1/tasks?all=1&lifecycle=completed&limit=5", tok, nil, &recent); st != http.StatusOK || len(recent.Tasks) != 5 || recent.Next == "" {
		t.Fatalf("recent completed = %d %+v", st, recent)
	}
	for _, query := range []string{"all=1&lifecycle=invalid", "all=1&lifecycle=", "all=1&lifecycle=queued,", "all=1&before=bad", "all=1&before=0:id", "all=1&limit=0", "all=1&limit=51", "all=1&limit=invalid"} {
		if st := e.do("GET", "/api/v1/tasks?"+query, tok, nil, nil); st != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", query, st)
		}
	}
}

func TestTaskDetailAssociatesRunsWithTheirImmutableSpec(t *testing.T) {
	e := newEnv(t)
	e.configure()
	tok := e.login(protocol.ScopeOwner)
	id := e.task("changing spec")
	old := e.startRun(id)
	revision := int64(1)
	e.exec("tasks.update_intent", protocol.Target{Type: "task", ID: id}, &revision, map[string]any{
		"spec": domain.Spec{Goal: "new goal", Acceptance: []string{"new result"}, Delivery: domain.DeliveryCommit,
			Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}},
	}, app.UpdateIntent{})
	current := e.startRun(id)
	var task protocol.TaskDetail
	if st := e.do("GET", "/api/v1/tasks/"+id, tok, nil, &task); st != http.StatusOK {
		t.Fatalf("task detail = %d", st)
	}
	if len(task.Runs) != 2 || task.SpecVersion != 2 {
		t.Fatalf("task = %+v", task)
	}
	versions := map[string]int64{}
	for _, run := range task.Runs {
		if run.Generation != 1 {
			t.Fatalf("expected reused generation one: %+v", run)
		}
		versions[run.RunID] = run.SpecVersion
	}
	if versions[old] != 1 || versions[current] != 2 {
		t.Fatalf("run spec identity = %+v", versions)
	}
}

func TestTaskDetailReadsReferencesAndBacklinks(t *testing.T) {
	e := newEnv(t)
	tok := e.login(protocol.ScopeOwner)
	e.configure()
	parent := e.task("original")
	child := e.task("followup")
	e.exec("tasks.update_intent", protocol.Target{Type: "task", ID: child}, new(int64(1)), map[string]any{"relations": []protocol.TaskRelation{{Kind: protocol.RelationFollowUp, TaskID: parent}}}, app.UpdateIntent{})
	for _, tc := range []struct {
		id, other string
		direction protocol.RelationDirection
	}{{parent, child, protocol.RelationIncoming}, {child, parent, protocol.RelationOutgoing}} {
		var d protocol.TaskDetail
		if st := e.do("GET", "/api/v1/tasks/"+tc.id, tok, nil, &d); st != http.StatusOK {
			t.Fatal(st)
		}
		if len(d.Relations) != 1 || d.Relations[0].Task.TaskID != tc.other || d.Relations[0].Direction != tc.direction || d.Relations[0].Kind != protocol.RelationFollowUp {
			t.Fatalf("relations=%+v", d.Relations)
		}
	}
}

func TestCrossProjectBacklinkRemovalReachesAgentDelta(t *testing.T) {
	e := newEnv(t)
	e.configureProjects()
	core := e.sessionIn("s-core", "https://example.com/me/tend")
	web := e.sessionIn("s-web", "git@example.com:me/site.git")
	coreTask := created(t, core.call("tasks_create", createArgs("ledger", "ledger"), ""))
	webTask := created(t, web.call("tasks_create", createArgs("site", "site"), ""))
	revision := e.taskRow(webTask).ControlRevision
	e.exec("tasks.update_intent", protocol.Target{Type: "task", ID: webTask}, &revision, map[string]any{
		"relations": []protocol.TaskRelation{{Kind: protocol.RelationRelated, TaskID: coreTask}},
	}, app.UpdateIntent{})
	initial := core.start("startup")
	if !strings.Contains(initial, "related incoming: "+webTask) {
		t.Fatalf("initial backlink missing: %s", initial)
	}
	revision = e.taskRow(webTask).ControlRevision
	e.exec("tasks.update_intent", protocol.Target{Type: "task", ID: webTask}, &revision, map[string]any{
		"relations": []protocol.TaskRelation{},
	}, app.UpdateIntent{})
	next := core.prompt()
	if !strings.Contains(next, "task.relations_backlinks_updated") || !strings.Contains(next, webTask) {
		t.Fatalf("backlink removal missing from delta: %s", next)
	}
}

// Completion order must not drift when an old task's metadata is changed.
func TestRecentTasksUseCompletionEvidenceAndPrimaryWorkItem(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	ids := []string{e.task("old result"), e.task("new result"), e.task("legacy result"), e.task("another legacy result"), e.task("cancelled result")}
	err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		for i, id := range ids {
			state := "completed"
			if i == 4 {
				state = "cancelled"
			}
			if _, err := tx.Exec(t.Context(), "UPDATE tasks SET lifecycle=?, updated_at=? WHERE task_id=?", state, 9000-i, id); err != nil {
				return err
			}
			if i < 2 || i == 4 {
				if _, err := tx.InsertEvent(t.Context(), store.EventRow{EventID: fmt.Sprintf("event_completion_%d", i), Source: "app", ObjectType: "task", ObjectID: id, Kind: "task.completed", BodyJSON: "{}", CreatedAt: int64(1000 + i*1000)}); err != nil {
					return err
				}
			}
		}
		owner, err := store.GetTaskWorkItemOwner(t.Context(), tx.Q(), ids[1])
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), "UPDATE work_items SET body_json=json_set(body_json,'$.title','Current parent title') WHERE work_item_id=?", owner)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []protocol.TaskSummary
	cursor := ""
	for range 5 {
		var page protocol.TaskPage
		path := "/api/v1/tasks?all=1&lifecycle=completed&sort=completed_at&limit=1"
		if cursor != "" {
			path += "&before=" + url.QueryEscape(cursor)
		}
		if status := e.do("GET", path, token, nil, &page); status != http.StatusOK {
			t.Fatalf("page status %d", status)
		}
		got = append(got, page.Tasks...)
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(got) != 4 || got[0].TaskID != ids[1] || got[1].TaskID != ids[0] || got[0].CompletedAt != 2000 || got[1].CompletedAt != 1000 {
		t.Fatalf("completion order: %+v", got)
	}
	if got[0].PrimaryWorkItemID == "" || got[0].PrimaryWorkItemTitle != "Current parent title" {
		t.Fatalf("primary item: %+v", got[0])
	}
	if got[2].CompletedAt != 0 || got[3].CompletedAt != 0 || got[2].TaskID == got[3].TaskID {
		t.Fatalf("missing historical evidence: %+v", got[2:])
	}
	var detail protocol.TaskDetail
	if status := e.do("GET", "/api/v1/tasks/"+ids[1], token, nil, &detail); status != http.StatusOK || detail.CompletedAt != got[0].CompletedAt {
		t.Fatalf("detail mismatch: %d %+v", status, detail)
	}
	for _, path := range []string{"/api/v1/tasks?all=1&sort=completed_at", "/api/v1/tasks?all=1&lifecycle=running&sort=completed_at", "/api/v1/tasks?all=1&sort=invalid"} {
		if status := e.do("GET", path, token, nil, nil); status != http.StatusBadRequest {
			t.Fatalf("invalid sort %s: %d", path, status)
		}
	}
}
