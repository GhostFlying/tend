package app

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueMaterialsProjectDiscoveryPagesWithoutExpandingOverview(t *testing.T) {
	h := newDeliverableHarness(t)
	var issues []protocol.Issue
	for range 6 {
		issues = append(issues, createCanonicalOwnerTestIssue(t, h))
	}
	root := issues[0]
	page, err := h.app.ReadIssueMaterials(t.Context(), root.IssueID, protocol.IssueMaterialsQuery{Limit: 1})
	if err != nil || len(page.Issues) != 1 || page.Issues[0].IssueID != root.IssueID || len(page.IssueIDs) != 1 {
		t.Fatal("overview expanded the whole project", page, err)
	}
	var seen []string
	after := ""
	for {
		page, err = h.app.ReadIssueMaterials(t.Context(), root.IssueID, protocol.IssueMaterialsQuery{Kind: "issues", After: after, Limit: 2})
		if err != nil || len(page.Issues) > 2 {
			t.Fatal(page, err)
		}
		for _, issue := range page.Issues {
			if len(issue.Tasks) != 0 || issue.Attentions != nil || issue.Manager != nil {
				t.Fatal("discovery loaded Issue detail aggregates", issue)
			}
			if slices.Contains(seen, issue.IssueID) {
				t.Fatal("duplicate Issue across pages", issue.IssueID)
			}
			seen = append(seen, issue.IssueID)
		}
		if !page.More {
			break
		}
		if page.Next == "" || page.Next == after {
			t.Fatal("missing advancing cursor", page)
		}
		after = page.Next
	}
	if len(seen) != 6 {
		t.Fatal("project discovery lost Issues", seen)
	}
	materialProjectForTest(t, h, root.IssueID, "")
	page, err = h.app.ReadIssueMaterials(t.Context(), root.IssueID, protocol.IssueMaterialsQuery{Kind: "issues", Limit: 1})
	if err != nil || len(page.Issues) != 1 || page.Issues[0].IssueID != root.IssueID || page.More {
		t.Fatal("empty project expanded globally", page, err)
	}
}

func TestIssueMaterialsKnownFileAndSelectedIssueAvoidUnrelatedTaskContexts(t *testing.T) {
	h := newDeliverableHarness(t)
	root := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, root)
	as, ok, _ := h.claim("linux-1")
	if !ok || as.TaskID != task {
		t.Fatal(as)
	}
	other := createCanonicalOwnerTestIssue(t, h)
	file := uploadChatFile(t, h, "known.txt", "text/plain", []byte("known same-project original"))
	materialMessage(t, h, other.IssueID, file.FileID)
	unrelated := createCanonicalOwnerTestIssue(t, h)
	unrelatedTask := createCanonicalTaskForTest(t, h, unrelated)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		// An unrelated retained Task's typed context must not be decoded to find
		// a message original that already has an exact persisted source.
		raw, err := json.Marshal([]map[string]any{{"work_item_id": unrelated.IssueID, "facts": "unrelated legacy payload"}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE task_specs SET body_json=json_set(body_json,'$.shared_context',json(?)) WHERE task_id=?`, string(raw), unrelatedTask)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := runPrincipal(as.RunID, as.Role)
	metadata, err := h.app.ReadRunContextFile(t.Context(), p, file.FileID)
	if err != nil || metadata.SHA256 != file.SHA256 {
		t.Fatal("exact file loaded unrelated project history", metadata, err)
	}
	beforeEvents, beforeRequests := h.count("events"), h.count("request_records")
	exact, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "files", ID: file.FileID})
	if err != nil || len(exact.Materials.Files) != 1 || exact.Materials.Files[0].SHA256 != file.SHA256 {
		t.Fatal("index and exact metadata disagree", exact, err)
	}
	page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{IssueID: other.IssueID, Kind: "files"})
	if err != nil || len(page.Materials.Files) != 1 || page.Materials.Files[0].FileID != file.FileID || !slices.Contains(page.Materials.IssueIDs, other.IssueID) || slices.Contains(page.Materials.IssueIDs, unrelated.IssueID) {
		t.Fatal("explicit Issue selection did not stay bounded", page, err)
	}
	if beforeEvents != h.count("events") || beforeRequests != h.count("request_records") {
		t.Fatal("discovery created a grant or business write")
	}
	if _, err := h.app.ReadIssueMaterials(t.Context(), root.IssueID, protocol.IssueMaterialsQuery{IssueID: other.IssueID}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("owner URL target was silently replaced", err)
	}
	materialProjectForTest(t, h, other.IssueID, "isolated")
	if _, err := h.app.ReadRunContextFile(t.Context(), p, file.FileID); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("cross-project original became public", err)
	}
	if _, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{IssueID: other.IssueID}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("cross-project selection became public", err)
	}
}
