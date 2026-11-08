package app

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestArtifactShareGrantRevokePreservesImmutableResultAndRequest(t *testing.T) {
	h := newDeliverableHarness(t)
	source := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, source)
	as, ok, op := h.claim("linux-1")
	if !ok || as.TaskID != task {
		t.Fatal(as, op)
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	original := []byte("# Original passed report\nEvidence remains byte-identical.\n")
	file, _ := h.uploadResultFile(as, "report.md", original)
	unused, _ := h.uploadResultFile(as, "private-draft.md", []byte("unshared upload"))
	payload := testArtifact(h.task(task).WorkspaceName)
	payload.Files = []protocol.ArtifactFile{file}
	payload.Report = &protocol.ResultReport{Body: "Original report"}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, payload, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifactID := h.task(task).CurrentArtifactID
	materialProjectForTest(t, h, source.IssueID, "separate-source")
	accept, ok, op := h.claim("linux-1")
	if !ok || accept.Role != protocol.RoleAccept {
		t.Fatal(accept, op)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	target := createCanonicalOwnerTestIssue(t, h)
	var artifact store.ArtifactRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var e error
		artifact, e = store.GetArtifact(t.Context(), tx.Q(), artifactID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	readable := func(want bool) {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			allowed, err := store.ArtifactShareReadable(t.Context(), tx.Q(), target.IssueID, artifactID, target.AssignmentEpoch)
			if err != nil {
				return err
			}
			if allowed != want {
				t.Fatal("artifact permission", allowed, want)
			}
			allowed, err = store.ArtifactFileSharedWithIssue(t.Context(), tx.Q(), target.IssueID, target.AssignmentEpoch, file)
			if err != nil {
				return err
			}
			if allowed != want {
				t.Fatal("file permission", allowed, want)
			}
			allowed, err = store.ArtifactFileSharedWithIssue(t.Context(), tx.Q(), target.IssueID, target.AssignmentEpoch, unused)
			if allowed {
				t.Fatal("unsubmitted sibling upload leaked")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	readable(false)
	grant := protocol.GrantArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "Use the original passed report in this responsibility"}, ArtifactID: artifactID, RefSHA256: store.ArtifactRefSHA256(artifact.RefJSON)}

	for _, kind := range []string{"wrong-hash", "stale-issue", "wrong-epoch", "source-revision-without-message"} {
		p := grant
		revision := target.Revision
		switch kind {
		case "wrong-hash":
			p.RefSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		case "stale-issue":
			revision++
		case "wrong-epoch":
			p.ExpectedAssignmentEpoch++
		case "source-revision-without-message":
			p.ExpectedSourceRevision = rev(1)
		}
		result := h.exec(owner, h.env(protocol.OpGrantArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, p, rev(revision)), ArtifactShareCommand{})
		if result.Status != protocol.OpFailed || h.count("artifact_shares") != 0 || canonicalTaskIssue(t, h, target.IssueID).Revision != target.Revision {
			t.Fatal("invalid sharing mutated target", kind, result)
		}
	}
	env := h.env(protocol.OpGrantArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, grant, rev(target.Revision))
	result := h.exec(owner, env, ArtifactShareCommand{})
	if result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	var share protocol.ArtifactShare
	if err := json.Unmarshal(result.Result, &share); err != nil {
		t.Fatal(err)
	}
	if !share.Applicable || share.SourceIssueID != source.IssueID || share.SourceTaskID != task || share.RefSHA256 != grant.RefSHA256 || len(share.Files) != 1 || share.Files[0] != file {
		t.Fatal(share)
	}
	readable(true)
	if replay := h.exec(owner, env, ArtifactShareCommand{}); !bytes.Equal(result.Result, replay.Result) || h.count("artifact_shares") != 1 {
		t.Fatal("request replay changed grant", replay)
	}

	changed := grant
	changed.Reason = "Different authorization under the same request"
	conflicting := h.env(protocol.OpGrantArtifactShare, env.Target, changed, env.ExpectedControlRevision)
	conflicting.RequestID = env.RequestID
	if _, err := h.app.Execute(t.Context(), owner, conflicting, ArtifactShareCommand{}); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("original identity accepted changed input", err)
	}

	// Exercise the access policy against an authoritative changed epoch. The
	// store fixture does not claim to test the separate assignment workflow.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		next, err := store.GetCanonicalIssue(t.Context(), tx.Q(), target.IssueID)
		if err != nil {
			return err
		}
		expected := next.Revision
		next.AssignmentEpoch++
		next.Revision++
		return tx.SaveCanonicalIssue(t.Context(), next, expected, store.IssueRevisionProvenance{Actor: owner, RequestID: h.ids.New(protocol.PrefixRequest), Reason: "Responsibility replacement fixture"})
	}); err != nil {
		t.Fatal(err)
	}
	target = canonicalTaskIssue(t, h, target.IssueID)
	readable(true)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		v, err := store.GetArtifactShare(t.Context(), tx.Q(), share.ShareID)
		if v.Status != "active" || !v.Applicable {
			t.Fatal("responsibility handoff erased shared knowledge", v)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target = canonicalTaskIssue(t, h, target.IssueID)
	revoke := protocol.RevokeArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "Revoke future reads"}, ShareID: share.ShareID, ExpectedShareRevision: share.Revision}

	wrongRevoke := revoke
	wrongRevoke.ExpectedShareRevision++
	if result := h.exec(owner, h.env(protocol.OpRevokeArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, wrongRevoke, rev(target.Revision)), ArtifactShareCommand{Revoke: true}); result.Status != protocol.OpFailed || result.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("stale share revoke", result)
	}
	readable(true)
	closed := h.exec(owner, h.env(protocol.OpRevokeArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, revoke, rev(target.Revision)), ArtifactShareCommand{Revoke: true})
	if closed.Status != protocol.OpSucceeded {
		t.Fatal(closed)
	}
	readable(false)
	if replay := h.exec(owner, env, ArtifactShareCommand{}); !bytes.Equal(result.Result, replay.Result) {
		t.Fatal("revocation rewrote original success", replay)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		a, err := store.GetArtifact(t.Context(), tx.Q(), artifactID)
		if a != artifact {
			t.Fatal("source artifact changed", a)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	body, err := h.store.FileStore().Open(file.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("original bytes changed", err)
	}
	if h.count("artifact_share_revisions") != 2 || h.count("acceptances") != 1 {
		t.Fatal("history changed")
	}
}

func TestArtifactShareMainRelayRequiresCurrentAdmittedOwnerSource(t *testing.T) {
	h := hostedManagerHarness(t)
	source := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, source)
	as, ok, claimEnv := h.claim("linux-1")
	if !ok || as.TaskID != task {
		t.Fatal(as, claimEnv)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifactID := h.task(task).CurrentArtifactID
	materialProjectForTest(t, h, source.IssueID, "separate-source")
	var artifact store.ArtifactRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		artifact, err = store.GetArtifact(t.Context(), tx.Q(), artifactID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target := createCanonicalOwnerTestIssue(t, h)
	message, delivery := h.say("Explicitly share the original report with this responsibility")
	turn := startHostedReview(t, h, "main", delivery)
	base := protocol.GrantArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: message, ExpectedSourceRevision: rev(1)}, ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "Relay this exact owner approval"}, ArtifactID: artifactID, RefSHA256: store.ArtifactRefSHA256(artifact.RefJSON)}
	attempt := func(payload protocol.GrantArtifactSharePayload, seq int64) (protocol.Operation, error) {
		return h.app.Execute(t.Context(), mainAgent, h.env(protocol.OpGrantArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, payload, rev(target.Revision)), HostedCoordinationCommand{Command: ArtifactShareCommand{}, ExecutionID: turn.RequestID, EffectSeq: seq})
	}
	for n, kind := range []string{"missing-source", "wrong-source-revision", "unadmitted-source"} {
		p := base
		switch kind {
		case "missing-source":
			p.SourceMessageID = ""
			p.ExpectedSourceRevision = nil
		case "wrong-source-revision":
			p.ExpectedSourceRevision = rev(2)
		case "unadmitted-source":
			p.SourceMessageID = queueMessage(t, h, "A different unadmitted owner message")
		}
		op, err := attempt(p, int64(n+1))
		if (err == nil && op.Status != protocol.OpFailed) || h.count("artifact_shares") != 0 {
			t.Fatal(kind, op, err)
		}
	}
	op, err := attempt(base, 4)
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("admitted main owner relay rejected", op, err)
	}
	var share protocol.ArtifactShare
	if err := json.Unmarshal(op.Result, &share); err != nil {
		t.Fatal(err)
	}
	if share.SourceMessageID != message || share.SourceRevision == nil || *share.SourceRevision != 1 {
		t.Fatal("owner provenance lost", share)
	}
	// A withdrawn, unclaimed owner source cannot be substituted into a new
	// relay. The successful earlier grant remains immutable history.
	next := createCanonicalOwnerTestIssue(t, h)
	message = queueMessage(t, h, "Withdraw this separate owner authorization before admission")
	withdrawOwnerSource(t, h, message)
	p := base
	p.SourceMessageID = message
	p.ExpectedAssignmentEpoch = next.AssignmentEpoch
	target = next
	op, err = attempt(p, 5)
	if (err == nil && op.Status != protocol.OpFailed) || h.count("artifact_shares") != 1 {
		t.Fatal("withdrawn owner source granted evidence", op, err)
	}
}

func TestArtifactShareManagerDownloadAndFrozenWorkerInputSurviveRevocation(t *testing.T) {
	h, runtime, target, manager, _ := reviewManagerAssignment(t)
	source := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, source)
	producer, ok := h.claimWith("linux-1", h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil))
	if !ok || producer.TaskID != task {
		t.Fatal("source not claimed", producer)
	}
	bytesOriginal := []byte("original immutable cross-Issue report")
	file, _ := h.uploadResultFile(producer, "report.md", bytesOriginal)
	result := testArtifact(h.task(task).WorkspaceName)
	result.Files = []protocol.ArtifactFile{file}
	if op := h.runAs(producer, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifactID := h.task(task).CurrentArtifactID
	materialProjectForTest(t, h, source.IssueID, "separate-source")
	var artifact store.ArtifactRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		artifact, err = store.GetArtifact(t.Context(), tx.Q(), artifactID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	principal := runPrincipal(manager.RunID, protocol.RoleIssueManager)
	read := func(want bool) {
		t.Helper()
		meta, body, err := h.app.OpenRunFile(t.Context(), principal, file.FileID)
		if !want {
			if err == nil {
				body.Close()
				t.Fatal("unshared original exposed")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		defer body.Close()
		raw, err := io.ReadAll(body)
		if err != nil || meta != file || !bytes.Equal(raw, bytesOriginal) {
			t.Fatal("original download changed", meta, err)
		}
	}
	read(false)
	publication := h.exec(owner, h.env("messaging.notify", protocol.Target{Type: "message"}, protocol.MessagingPublication{Body: "Original source report", Purpose: "final", Reason: "verified_result", Attachments: []string{file.FileID}}, nil), MessagingCommand{Action: "notify"})
	if publication.Status != protocol.OpSucceeded || h.count("conversation_attachments") != 1 {
		t.Fatal("original report was not published as an attachment", publication)
	}
	read(false)
	target = canonicalTaskIssue(t, h, target.IssueID)
	op := h.exec(owner, h.env(protocol.OpGrantArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, protocol.GrantArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "Use exact original report"}, ArtifactID: artifactID, RefSHA256: store.ArtifactRefSHA256(artifact.RefJSON)}, rev(target.Revision)), ArtifactShareCommand{})
	var share protocol.ArtifactShare
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &share) != nil {
		t.Fatal(op)
	}
	read(true)
	// Explicitly admitted task inputs are immutable independent authority. A
	// later read-grant revocation never rewrites that already frozen contract.
	spec := temporaryReportSpec()
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
	consumer := h.createReportTask("consume authorized original", spec)
	saved := h.storedSpec(consumer.TaskID, 1)
	target = canonicalTaskIssue(t, h, target.IssueID)
	op = h.exec(owner, h.env(protocol.OpRevokeArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, protocol.RevokeArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "End new manager reads"}, ShareID: share.ShareID, ExpectedShareRevision: share.Revision}, rev(target.Revision)), ArtifactShareCommand{Revoke: true})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// The consumer's already saved input is now an independent project source;
	// revoking the original share does not erase that retained knowledge.
	read(true)
	if len(saved.Inputs) != 1 || saved.Inputs[0].SourceMessageID == "" {
		t.Fatal("published input did not retain its admitted message authority", saved)
	}
	if got := h.storedSpec(consumer.TaskID, 1); len(got.Inputs) != 1 || got.Inputs[0] != saved.Inputs[0] {
		t.Fatal("revocation rewrote admitted input", got)
	}
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityTaskInputs)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// First finish the source's independent acceptance, then claim the consumer.
	accept, ok := h.claimInputRun(runtime)
	if !ok || accept.TaskID != task || accept.Role != protocol.RoleAccept {
		t.Fatal(accept)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != consumer.TaskID {
		t.Fatal(run)
	}
	meta, body, err := h.app.OpenRunFile(t.Context(), runPrincipal(run.RunID, run.Role), file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil || meta != file || !bytes.Equal(raw, bytesOriginal) {
		t.Fatal("frozen input original lost", meta, err)
	}
}

func TestArtifactShareMissingReferencesPersistOriginalFailure(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	grant := protocol.GrantArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Inspect exact immutable original"}, ArtifactID: h.ids.New(protocol.PrefixArtifact), RefSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	revoke := protocol.RevokeArtifactSharePayload{IssueControlPayload: grant.IssueControlPayload, ShareID: h.ids.New(protocol.PrefixArtifactShare), ExpectedShareRevision: 1}
	for _, test := range []struct {
		name, issue string
		payload     any
		command     ArtifactShareCommand
	}{{"missing-issue", h.ids.New(protocol.PrefixWorkItem), grant, ArtifactShareCommand{}}, {"missing-artifact", issue.IssueID, grant, ArtifactShareCommand{}}, {"missing-share", issue.IssueID, revoke, ArtifactShareCommand{Revoke: true}}} {
		t.Run(test.name, func(t *testing.T) {
			env := h.env(test.command.Operation(), protocol.Target{Type: "issue", ID: test.issue}, test.payload, rev(issue.Revision))
			op, err := h.app.Execute(t.Context(), owner, env, test.command)
			if err != nil || op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeNotFound {
				t.Fatal("missing reference lost business receipt", op, err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				r, err := store.GetRequest(t.Context(), tx.Q(), owner.DedupDomain(), env.RequestID)
				if err == nil && (r.Status != protocol.OpFailed || r.ErrorCode != string(protocol.CodeNotFound)) {
					t.Fatal(r)
				}
				return err
			}); err != nil {
				t.Fatal("failure cannot be inspected", err)
			}
			replay, err := h.app.Execute(t.Context(), owner, env, test.command)
			if err != nil || !reflect.DeepEqual(op, replay) {
				t.Fatal("original failed identity was not replayed", replay, err)
			}
			if h.count("artifact_shares") != 0 || canonicalTaskIssue(t, h, issue.IssueID).Revision != issue.Revision {
				t.Fatal("missing reference mutated target")
			}
		})
	}
}

func TestArtifactShareAuthorizationSourceDoesNotGrantOtherAttachments(t *testing.T) {
	h, runtime, target, manager, _ := reviewManagerAssignment(t)
	inventoryCategory(t, h, "planner", 0, "active")
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	source := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, source)
	producer, ok := h.claimWith("linux-1", h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil))
	if !ok || producer.TaskID != task {
		t.Fatal("producer", producer)
	}
	selected, _ := h.uploadResultFile(producer, "selected-report.txt", []byte("THE EXACT ARTIFACT FILE"))
	result := testArtifact(h.task(task).WorkspaceName)
	result.Files = []protocol.ArtifactFile{selected}
	if op := h.runAs(producer, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifactID := h.task(task).CurrentArtifactID
	materialProjectForTest(t, h, source.IssueID, "separate-source")
	var artifact store.ArtifactRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		artifact, err = store.GetArtifact(t.Context(), tx.Q(), artifactID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	publication := h.exec(owner, h.env("messaging.notify", protocol.Target{Type: "message"}, protocol.MessagingPublication{Body: "Original report", Purpose: "final", Reason: "verified_result", Attachments: []string{selected.FileID}}, nil), MessagingCommand{Action: "notify"})
	if publication.Status != protocol.OpSucceeded {
		t.Fatal(publication)
	}
	// The manager fixture leaves unrelated main deliveries queued. Exclude
	// those setup messages before admitting this distinct owner authorization.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE destination='main_thread'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	private := uploadChatFile(t, h, "unshared-private.txt", "text/plain", []byte("NOT PART OF ARTIFACT"))
	msg := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Share only the specified artifact, not the attached private document", Attachments: []string{private.FileID, selected.FileID}}, nil), SubmitMessage{})
	var message struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(msg.Result, &message); err != nil || message.MessageID == "" {
		t.Fatal(msg, err)
	}
	delivery, _, ready, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ready {
		t.Fatal("delivery", ready, err)
	}
	turn := startHostedReview(t, h, "main", delivery)
	principal := runPrincipal(manager.RunID, protocol.RoleIssueManager)
	if _, err := h.app.ManagerFile(t.Context(), principal, private.FileID); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("before grant should deny", err)
	}
	target = canonicalTaskIssue(t, h, target.IssueID)
	control := protocol.IssueControlPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: message.MessageID, ExpectedSourceRevision: rev(1)}, ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "Share exact artifact only"}
	op := h.exec(mainAgent, h.env(protocol.OpGrantArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, protocol.GrantArtifactSharePayload{IssueControlPayload: control, ArtifactID: artifactID, RefSHA256: store.ArtifactRefSHA256(artifact.RefJSON)}, rev(target.Revision)), HostedCoordinationCommand{Command: ArtifactShareCommand{}, ExecutionID: turn.RequestID, EffectSeq: 1})
	var share protocol.ArtifactShare
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &share) != nil {
		t.Fatal(op)
	}
	metadata, err := h.app.ManagerFile(t.Context(), principal, private.FileID)
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("grant authorized unrelated source attachment", metadata, err)
	}
	if metadata, err = h.app.ManagerFile(t.Context(), principal, selected.FileID); err != nil || metadata.FileID != selected.FileID {
		t.Fatal("exact granted file was not readable", metadata, err)
	}
	if share.SourceMessageID != message.MessageID || share.SourceRevision == nil || *share.SourceRevision != 1 {
		t.Fatal("authorization provenance lost", share)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var materialSource, reason string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COALESCE(source_message_id,''),reason_json FROM issue_control_revisions WHERE issue_id=? AND revision=?`, target.IssueID, target.Revision+1).Scan(&materialSource, &reason); err != nil {
			return err
		}
		if materialSource != "" {
			t.Fatal("grant created general source authority", materialSource)
		}
		var saved protocol.IssueControlPayload
		if err := json.Unmarshal([]byte(reason), &saved); err != nil {
			return err
		}
		if saved.SourceMessageID != message.MessageID || saved.ExpectedSourceRevision == nil || *saved.ExpectedSourceRevision != 1 {
			t.Fatal("revision lost exact approval provenance", saved)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	target = canonicalTaskIssue(t, h, target.IssueID)
	op = h.exec(owner, h.env(protocol.OpRevokeArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, protocol.RevokeArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "Revoke future reads"}, ShareID: share.ShareID, ExpectedShareRevision: share.Revision}, rev(target.Revision)), ArtifactShareCommand{Revoke: true})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	metadata, err = h.app.ManagerFile(t.Context(), principal, private.FileID)
	if err == nil {
		t.Errorf("unshared private attachment remains readable even after revoke: %s", metadata.FileID)
	}
	metadata, err = h.app.ManagerFile(t.Context(), principal, selected.FileID)
	if err == nil {
		t.Fatalf("exact shared artifact file remains readable even after revoke: %s", metadata.FileID)
	}
}

func TestArtifactShareNativeAuthorityRejectsClassifiedHistory(t *testing.T) {
	h := hostedManagerHarness(t)
	source := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, source)
	as, ok, claim := h.claim("linux-1")
	if !ok || as.TaskID != task {
		t.Fatal(as, claim)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifactID := h.task(task).CurrentArtifactID
	materialProjectForTest(t, h, source.IssueID, "separate-source")
	var artifact store.ArtifactRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		artifact, err = store.GetArtifact(t.Context(), tx.Q(), artifactID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target := createCanonicalOwnerTestIssue(t, h)
	historical, delivery := h.say("Previous owner authorization, not admitted to next execution")
	previous := startHostedReview(t, h, "main", delivery)
	finishHostedReview(t, h, previous)
	currentSource, next := h.say("Share exact artifact in this current native execution")
	current := startHostedReview(t, h, "main", next)
	associations := h.associations(historical)
	classified := hostedEffect(t, h, mainAgent, current, 1, protocol.Target{Type: "message", ID: historical}, rev(associations.Revision), protocol.SetIssueMessageAssociationsPayload{Disposition: "unassigned", ContentRevision: associations.ContentRevision}, SetIssueMessageAssociations{})
	if classified.Status != protocol.OpSucceeded {
		t.Fatal(classified)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var member bool
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turn_messages m USING(logical_turn_id) WHERE e.request_id=? AND m.message_id=?)`, current.RequestID, historical).Scan(&member); err != nil {
			return err
		}
		if !member {
			t.Fatal("fixture did not classify historical source")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	control := func(message string) protocol.IssueControlPayload {
		return protocol.IssueControlPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: message, ExpectedSourceRevision: rev(1)}, ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "Relay exact original artifact"}
	}
	grant := protocol.GrantArtifactSharePayload{IssueControlPayload: control(historical), ArtifactID: artifactID, RefSHA256: store.ArtifactRefSHA256(artifact.RefJSON)}
	command := HostedCoordinationCommand{Command: ArtifactShareCommand{}, ExecutionID: current.RequestID, EffectSeq: 2}
	rejected, err := h.app.Execute(t.Context(), mainAgent, h.env(command.Operation(), protocol.Target{Type: "issue", ID: target.IssueID}, grant, rev(target.Revision)), command)
	if err == nil && rejected.Status == protocol.OpSucceeded || h.count("artifact_shares") != 0 {
		t.Fatal("classified history authorized grant", rejected, err)
	}
	grant.IssueControlPayload = control(currentSource)
	command.EffectSeq = 3
	request := h.env(command.Operation(), protocol.Target{Type: "issue", ID: target.IssueID}, grant, rev(target.Revision))
	accepted := h.exec(mainAgent, request, command)
	if accepted.Status != protocol.OpSucceeded {
		t.Fatal(accepted)
	}
	replay := h.exec(mainAgent, request, command)
	if !bytes.Equal(accepted.Result, replay.Result) {
		t.Fatal("original grant result changed")
	}
	var share protocol.ArtifactShare
	if err := json.Unmarshal(accepted.Result, &share); err != nil {
		t.Fatal(err)
	}
	revoke := protocol.RevokeArtifactSharePayload{IssueControlPayload: control(historical), ShareID: share.ShareID, ExpectedShareRevision: share.Revision}
	command = HostedCoordinationCommand{Command: ArtifactShareCommand{Revoke: true}, ExecutionID: current.RequestID, EffectSeq: 4}
	rejected, err = h.app.Execute(t.Context(), mainAgent, h.env(command.Operation(), protocol.Target{Type: "issue", ID: target.IssueID}, revoke, rev(target.Revision+1)), command)
	if err == nil && rejected.Status == protocol.OpSucceeded || h.count("artifact_share_revisions") != 1 {
		t.Fatal("classified history authorized revoke", rejected, err)
	}
	revoke.IssueControlPayload = control(currentSource)
	command.EffectSeq = 5
	request = h.env(command.Operation(), protocol.Target{Type: "issue", ID: target.IssueID}, revoke, rev(target.Revision+1))
	accepted = h.exec(mainAgent, request, command)
	if accepted.Status != protocol.OpSucceeded || h.count("artifact_share_revisions") != 2 {
		t.Fatal(accepted)
	}
	replay = h.exec(mainAgent, request, command)
	if !bytes.Equal(accepted.Result, replay.Result) {
		t.Fatal("original revoke result changed")
	}
}

func TestArtifactShareHistoricalInputsRemainIndependentSavedMaterials(t *testing.T) {
	h := hostedManagerHarness(t)
	runtime := testRuntime("input-share-boundary")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityTaskInputs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	source := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, source)
	producer, ok := h.claimInputRun(runtime)
	if !ok || producer.TaskID != task {
		t.Fatal(producer)
	}
	original := []byte("immutable exact report input")
	file, _ := h.uploadResultFile(producer, "report.txt", original)
	payload := testArtifact(h.task(task).WorkspaceName)
	payload.Files = []protocol.ArtifactFile{file}
	if op := h.runAs(producer, protocol.OpRunsSubmitArtifact, payload, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifactID := h.task(task).CurrentArtifactID
	materialProjectForTest(t, h, source.IssueID, "separate-source")
	acceptance, ok := h.claimInputRun(runtime)
	if !ok || acceptance.Role != protocol.RoleAccept {
		t.Fatal(acceptance)
	}
	if op := h.runAs(acceptance, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	target := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, manager := startHostedIssueReview(t, h, target)
	var artifact store.ArtifactRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		artifact, err = store.GetArtifact(t.Context(), tx.Q(), artifactID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	seq := int64(0)
	create := func(title string, inputs []protocol.TaskInput) (protocol.Operation, *protocol.Envelope, HostedCoordinationCommand) {
		t.Helper()
		target = canonicalTaskIssue(t, h, target.IssueID)
		spec := testSpec
		spec.Inputs = inputs
		seq++
		command := HostedCoordinationCommand{Command: CanonicalOwnerTaskCreate{}, ExecutionID: turn.RequestID, EffectSeq: seq}
		request := h.env(command.Operation(), protocol.Target{Type: "issue", ID: target.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Title: title, Spec: spec, Rating: protocol.RatingP1}, rev(target.Revision))
		return h.exec(manager, request, command), request, command
	}
	selected := []protocol.TaskInput{{FileID: file.FileID}}
	beforeTasks, beforeSpecs := h.count("tasks"), h.count("task_specs")
	denied, _, _ := create("No grant yet", selected)
	expectWorkCode(t, denied, protocol.CodeInvalidRequest)
	if h.count("tasks") != beforeTasks || h.count("task_specs") != beforeSpecs {
		t.Fatal("ungranted input created partial task")
	}
	target = canonicalTaskIssue(t, h, target.IssueID)
	granted := h.exec(owner, h.env(protocol.OpGrantArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, protocol.GrantArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "Allow exact original input"}, ArtifactID: artifactID, RefSHA256: store.ArtifactRefSHA256(artifact.RefJSON)}, rev(target.Revision)), ArtifactShareCommand{})
	var share protocol.ArtifactShare
	if granted.Status != protocol.OpSucceeded || json.Unmarshal(granted.Result, &share) != nil {
		t.Fatal(granted)
	}
	frozen, _, _ := create("Freeze while grant is active", selected)
	if frozen.Status != protocol.OpSucceeded {
		t.Fatal(frozen)
	}
	var consumer struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(frozen.Result, &consumer); err != nil {
		t.Fatal(err)
	}
	target = canonicalTaskIssue(t, h, target.IssueID)
	seq++
	if op := hostedEffect(t, h, manager, turn, seq, protocol.Target{Type: "issue", ID: target.IssueID}, nil, hostedReviewPayload(t, h, target), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	worker, ok := h.claimInputRun(runtime)
	if !ok || worker.TaskID != consumer.TaskID {
		t.Fatal(worker, consumer)
	}
	empty, _, _ := create("No original input in this contract", nil)
	if empty.Status != protocol.OpSucceeded {
		t.Fatal(empty)
	}
	var editable struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(empty.Result, &editable); err != nil {
		t.Fatal(err)
	}
	target = canonicalTaskIssue(t, h, target.IssueID)
	revoked := h.exec(owner, h.env(protocol.OpRevokeArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, protocol.RevokeArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "End new original-input access"}, ShareID: share.ShareID, ExpectedShareRevision: share.Revision}, rev(target.Revision)), ArtifactShareCommand{Revoke: true})
	if revoked.Status != protocol.OpSucceeded {
		t.Fatal(revoked)
	}
	beforeTasks, beforeSpecs = h.count("tasks"), h.count("task_specs")
	reused, request, command := create("Reuse the original already saved in this Issue's frozen input history", selected)
	if reused.Status != protocol.OpSucceeded {
		t.Fatal("share revocation erased independently retained inputs", reused)
	}
	replayed := h.exec(manager, request, command)
	if !reflect.DeepEqual(reused, replayed) || h.count("tasks") != beforeTasks+1 || h.count("task_specs") != beforeSpecs+1 {
		t.Fatal("original input reuse was not atomic/idempotent", reused, replayed)
	}
	spec := testSpec
	spec.Inputs = selected
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var ownerSpec protocol.OwnerTaskSpec
	if err := json.Unmarshal(raw, &ownerSpec); err != nil {
		t.Fatal(err)
	}
	seq++
	update := HostedCoordinationCommand{Command: CanonicalOwnerTaskUpdate{}, ExecutionID: turn.RequestID, EffectSeq: seq}
	request = h.env(update.Operation(), taskTarget(editable.TaskID), protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Spec: &ownerSpec}, rev(h.task(editable.TaskID).ControlRevision))
	reused = h.exec(manager, request, update)
	if reused.Status != protocol.OpSucceeded {
		t.Fatal("available saved material required another grant", reused)
	}
	replayed = h.exec(manager, request, update)
	if !reflect.DeepEqual(reused, replayed) || h.count("task_specs") != beforeSpecs+2 || len(h.storedSpec(editable.TaskID, 1).Inputs) != 0 || len(h.storedSpec(editable.TaskID, 2).Inputs) != 1 {
		t.Fatal("saved input reuse changed its original spec or request identity", reused, replayed)
	}
	// Existing contracts retain the bytes they were lawfully granted earlier.
	metadata, body, err := h.app.OpenRunFile(t.Context(), runPrincipal(worker.RunID, worker.Role), file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	actual, readErr := io.ReadAll(body)
	body.Close()
	if readErr != nil || metadata.SHA256 != file.SHA256 || !bytes.Equal(actual, original) {
		t.Fatal(metadata, actual, readErr)
	}
	oldInput := h.storedSpec(consumer.TaskID, 1).Inputs[0]
	ownerSpec.Goal = "Refine the same already-authorized contract"
	seq++
	retained := hostedEffect(t, h, manager, turn, seq, taskTarget(consumer.TaskID), rev(h.task(consumer.TaskID).ControlRevision), protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Spec: &ownerSpec}, CanonicalOwnerTaskUpdate{})
	if retained.Status != protocol.OpSucceeded {
		t.Fatal("retained input was revoked retroactively", retained)
	}
	if got := h.storedSpec(consumer.TaskID, h.task(consumer.TaskID).SpecVersion).Inputs; len(got) != 1 || got[0] != oldInput {
		t.Fatal("retained original metadata changed", got)
	}
	// An explicitly associated owner attachment remains a separate lawful source.
	own := uploadChatFile(t, h, "own.txt", "text/plain", []byte("target Issue material"))
	message := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Own Issue original", Attachments: []string{own.FileID}}, nil), SubmitMessage{})
	var attached struct {
		MessageID string `json:"message_id"`
	}
	if json.Unmarshal(message.Result, &attached) != nil || attached.MessageID == "" {
		t.Fatal(message)
	}
	associations := h.associations(attached.MessageID)
	if linked := h.exec(owner, h.env("conversation.set_issue_associations", protocol.Target{Type: "message", ID: attached.MessageID}, protocol.SetIssueMessageAssociationsPayload{Disposition: "linked", ContentRevision: associations.ContentRevision, IssueIDs: []string{target.IssueID}}, rev(associations.Revision)), SetIssueMessageAssociations{}); linked.Status != protocol.OpSucceeded {
		t.Fatal(linked)
	}
	ownTask, _, _ := create("Use lawful same Issue attachment", []protocol.TaskInput{{FileID: own.FileID}})
	if ownTask.Status != protocol.OpSucceeded {
		t.Fatal(ownTask)
	}
}

func TestArtifactSharePublishedSameIssueInputRetainsIndependentAuthority(t *testing.T) {
	h := hostedManagerHarness(t)
	runtime := testRuntime("same-issue-publication")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityTaskInputs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	producer, ok := h.claimInputRun(runtime)
	if !ok || producer.TaskID != task {
		t.Fatal(producer)
	}
	file, _ := h.uploadResultFile(producer, "same-issue-report.txt", []byte("same Issue original"))
	result := testArtifact(h.task(task).WorkspaceName)
	result.Files = []protocol.ArtifactFile{file}
	if op := h.runAs(producer, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	acceptance, ok := h.claimInputRun(runtime)
	if !ok || acceptance.Role != protocol.RoleAccept {
		t.Fatal(acceptance)
	}
	if op := h.runAs(acceptance, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = assignHostedManager(t, h, canonicalTaskIssue(t, h, issue.IssueID))
	turn, manager := startHostedIssueReview(t, h, issue)
	create := func(seq int64) protocol.Operation {
		issue = canonicalTaskIssue(t, h, issue.IssueID)
		spec := testSpec
		spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
		return hostedEffect(t, h, manager, turn, seq, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Use own original", Spec: spec, Rating: protocol.RatingP1}, CanonicalOwnerTaskCreate{})
	}
	if op := create(1); op.Status != protocol.OpSucceeded {
		t.Fatal("before publication", op)
	}
	publication := h.exec(owner, h.env("messaging.notify", protocol.Target{Type: "message"}, protocol.MessagingPublication{Body: "Original report", Purpose: "final", Reason: "verified_result", Attachments: []string{file.FileID}}, nil), MessagingCommand{Action: "notify"})
	if publication.Status != protocol.OpSucceeded {
		t.Fatal(publication)
	}
	if op := create(2); op.Status != protocol.OpSucceeded {
		t.Fatal("same-Issue file stopped being a legal input after publication", op)
	}
}
