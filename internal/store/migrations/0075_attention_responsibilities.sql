-- Keep problem handling separate from the original human-facing obligation.
CREATE TABLE run_question_contracts (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 supervisor_allowed INTEGER NOT NULL CHECK(supervisor_allowed IN (0,1))
) STRICT;
-- Recovery cannot silently change an already running worker's authority.
INSERT INTO run_question_contracts SELECT run_id,0 FROM runs;
CREATE TABLE attention_responsibilities (
 attention_id TEXT PRIMARY KEY REFERENCES attentions(attention_id),
 origin_issue_id TEXT NOT NULL REFERENCES issue_controls(issue_id),
 issue_id TEXT REFERENCES issue_controls(issue_id),
 recipient_kind TEXT NOT NULL CHECK(recipient_kind IN ('owner','agent','main')),
 recipient_ref TEXT NOT NULL,
 assignment_epoch INTEGER NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 body_json TEXT NOT NULL,
 notified_revision INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX attention_responsibility_recipient ON attention_responsibilities(recipient_kind,issue_id,attention_id);
CREATE TABLE attention_responsibility_history (
 attention_id TEXT NOT NULL REFERENCES attentions(attention_id),
 revision INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 request_id TEXT NOT NULL,
 PRIMARY KEY(attention_id,revision)
) STRICT;
CREATE TRIGGER attention_responsibility_history_update BEFORE UPDATE ON attention_responsibility_history BEGIN SELECT RAISE(ABORT,'responsibility history is immutable'); END;
CREATE TRIGGER attention_responsibility_history_delete BEFORE DELETE ON attention_responsibility_history BEGIN SELECT RAISE(ABORT,'responsibility history is immutable'); END;

-- Upgrade unresolved questions without rewriting original question/answer history.
INSERT INTO attention_responsibilities(attention_id,origin_issue_id,issue_id,recipient_kind,recipient_ref,assignment_epoch,revision,body_json,notified_revision)
 SELECT a.attention_id,c.issue_id,c.issue_id,c.assignee_kind,c.assignee_ref,c.assignment_epoch,1,
 json_object('ancestry',json((WITH RECURSIVE chain(id,parent,epoch,depth) AS (SELECT issue_id,parent_issue_id,assignment_epoch,0 FROM issue_controls WHERE issue_id=c.issue_id UNION ALL SELECT p.issue_id,p.parent_issue_id,p.assignment_epoch,chain.depth+1 FROM issue_controls p JOIN chain ON p.issue_id=chain.parent) SELECT json_group_array(json_object('issue_id',id,'assignment_epoch',epoch)) FROM (SELECT id,epoch FROM chain ORDER BY depth))),'origin_issue_id',c.issue_id,'origin_assignment_epoch',c.assignment_epoch,'issue_id',c.issue_id,'recipient_kind',c.assignee_kind,'recipient_ref',c.assignee_ref,'assignment_epoch',c.assignment_epoch,'revision',1,'history',json_array()),
 CASE WHEN c.assignee_kind='owner' THEN 1 ELSE 0 END
 FROM attentions a
 LEFT JOIN decisions d ON d.decision_id=a.decision_id
 LEFT JOIN worker_questions w ON w.decision_id=d.decision_id
 LEFT JOIN run_issue_responsibilities r ON r.run_id=w.run_id
 LEFT JOIN task_owner_contracts t ON t.task_id=d.task_id
 JOIN issue_controls c ON c.issue_id=COALESCE(r.issue_id,CASE WHEN EXISTS(SELECT 1 FROM json_each(d.effects_json) WHERE value='accept') THEN t.review_issue_id END,a.issue_id)
 WHERE a.status='open' AND a.kind IN ('decision','action') AND c.state IN ('active','paused');
INSERT INTO attention_responsibility_history(attention_id,revision,body_json,request_id)
 SELECT attention_id,revision,body_json,'migration:75' FROM attention_responsibilities;
