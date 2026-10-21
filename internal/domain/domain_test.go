package domain

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

var testWorkspace = Workspace{Mounts: []Mount{{Repo: "tend"}}}

var execSpec = Spec{Goal: "fix the windows build", Acceptance: []string{"build passes"}, Delivery: DeliveryCommit, Workspace: testWorkspace}

func changedSpec() Spec {
	s := execSpec
	s.Constraints = []string{"keep the public API"}
	return s
}

// taskIn returns a consistent task in the given lifecycle stage.
func taskIn(l protocol.TaskLifecycle) Task {
	t := Task{
		ID: "task_1", Lifecycle: l, Control: protocol.ControlManaged, ControlRevision: 5,
		SpecVersion: 1, Block: protocol.BlockNone, ImplementGeneration: 1, Budget: 3,
	}
	switch l {
	case protocol.TaskDraft:
		t.ImplementGeneration = 0
	case protocol.TaskAwaitingAcceptance:
		t.CurrentArtifactID = "art_1"
	case protocol.TaskChangesRequired:
		t.ImplementGeneration = 2
	}
	return t
}

func implementRun(t Task) RunFact {
	return RunFact{RunID: "run_i", Role: protocol.RoleImplement, SpecVersion: t.SpecVersion, Generation: t.ImplementGeneration}
}

func verdictOn(t Task, v protocol.Verdict) AcceptanceFact {
	return AcceptanceFact{RunID: "run_a", Role: protocol.RoleAccept, SpecVersion: t.SpecVersion, ArtifactID: t.CurrentArtifactID, Verdict: v}
}

type command struct {
	name string
	run  func(Task) (Decision, error)
}

var commands = []command{
	{"update spec", func(t Task) (Decision, error) { return UpdateSpec(t, changedSpec(), execSpec, t.ControlRevision) }},
	{"set priority", func(t Task) (Decision, error) { return SetPriority(t, t.Priority+1, t.ControlRevision) }},
	{"pause", func(t Task) (Decision, error) { return SetControlMode(t, protocol.ControlPaused, t.ControlRevision) }},
	{"cancel", func(t Task) (Decision, error) { return Cancel(t, t.ControlRevision) }},
	{"run started", func(t Task) (Decision, error) { return RunStarted(t, implementRun(t)), nil }},
	{"artifact", func(t Task) (Decision, error) { return ArtifactSubmitted(t, implementRun(t), "art_2"), nil }},
	{"passed", func(t Task) (Decision, error) { return AcceptanceSubmitted(t, verdictOn(t, protocol.VerdictPassed)) }},
	{"changes", func(t Task) (Decision, error) {
		return AcceptanceSubmitted(t, verdictOn(t, protocol.VerdictChangesRequired))
	}},
	{"blocked", func(t Task) (Decision, error) { return AcceptanceSubmitted(t, verdictOn(t, protocol.VerdictBlocked)) }},
}

// TestLifecycleTable checks every (stage, command) pair. A lifecycle value
// is the expected resulting stage; an error code means the command must be
// rejected with that code.
func TestLifecycleTable(t *testing.T) {
	const (
		draft     = protocol.TaskDraft
		queued    = protocol.TaskQueued
		running   = protocol.TaskRunning
		awaiting  = protocol.TaskAwaitingAcceptance
		changes   = protocol.TaskChangesRequired
		completed = protocol.TaskCompleted
		cancelled = protocol.TaskCancelled
	)
	invalid := protocol.CodeInvalidTransition
	table := map[protocol.TaskLifecycle][]any{
		//         update  priority pause    cancel     run      artifact  passed     changes  blocked
		draft:     {queued, draft, draft, cancelled, draft, draft, draft, draft, draft},
		queued:    {queued, queued, queued, cancelled, running, queued, queued, queued, queued},
		running:   {queued, running, running, cancelled, running, awaiting, running, running, running},
		awaiting:  {queued, awaiting, awaiting, cancelled, awaiting, awaiting, completed, changes, awaiting},
		changes:   {queued, changes, changes, cancelled, running, changes, changes, changes, changes},
		completed: {invalid, invalid, invalid, invalid, completed, completed, completed, completed, completed},
		cancelled: {invalid, invalid, invalid, invalid, cancelled, cancelled, cancelled, cancelled, cancelled},
	}
	for stage, row := range table {
		for i, cmd := range commands {
			t.Run(string(stage)+"/"+cmd.name, func(t *testing.T) {
				d, err := cmd.run(taskIn(stage))
				switch want := row[i].(type) {
				case protocol.Code:
					if got := protocol.CodeOf(err); err == nil || got != want {
						t.Fatalf("error = %v, want code %q", err, want)
					}
				case protocol.TaskLifecycle:
					if err != nil {
						t.Fatalf("error = %v, want stage %q", err, want)
					}
					if d.Task.Lifecycle != want {
						t.Errorf("stage = %q, want %q", d.Task.Lifecycle, want)
					}
				}
			})
		}
	}
}

func TestNewTask(t *testing.T) {
	d, err := NewTask("task_1", "p", execSpec, 2)
	if err != nil {
		t.Fatal(err)
	}
	if d.Task.Lifecycle != protocol.TaskQueued || len(d.Actions) != 1 || d.Actions[0].EffectKey() != "implement|task_1|1|-|1" {
		t.Errorf("executable spec: stage %q, actions %+v", d.Task.Lifecycle, d.Actions)
	}

	d, err = NewTask("task_2", "p", Spec{Goal: "think about it", Delivery: DeliveryCommit, Workspace: testWorkspace}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.Task.Lifecycle != protocol.TaskDraft || len(d.Actions) != 0 {
		t.Errorf("non-executable spec: stage %q, actions %+v", d.Task.Lifecycle, d.Actions)
	}

	if _, err := NewTask("task_3", "p", Spec{Goal: "x", Acceptance: []string{"y"}, Delivery: "release"}, 0); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Errorf("unsupported delivery: err = %v", err)
	}
}

func TestStaleRevisionRejected(t *testing.T) {
	task := taskIn(protocol.TaskRunning)
	stale := task.ControlRevision - 1
	for name, run := range map[string]func() error{
		"update spec": func() error { _, err := UpdateSpec(task, changedSpec(), execSpec, stale); return err },
		"priority":    func() error { _, err := SetPriority(task, 9, stale); return err },
		"control":     func() error { _, err := SetControlMode(task, protocol.ControlHumanControl, stale); return err },
		"cancel":      func() error { _, err := Cancel(task, stale); return err },
	} {
		if err := run(); protocol.CodeOf(err) != protocol.CodeStaleRevision {
			t.Errorf("%s with stale revision: err = %v, want stale_revision", name, err)
		}
	}
}

func TestUpdateSpec(t *testing.T) {
	task := taskIn(protocol.TaskAwaitingAcceptance)
	d, err := UpdateSpec(task, execSpec, execSpec, task.ControlRevision)
	if err != nil || d.Task != task || len(d.Events) != 0 {
		t.Errorf("equal spec should be a no-op: task %+v, events %v, err %v", d.Task, d.Events, err)
	}

	d, err = UpdateSpec(task, changedSpec(), execSpec, task.ControlRevision)
	if err != nil {
		t.Fatal(err)
	}
	got := d.Task
	if got.SpecVersion != 2 || got.ControlRevision != task.ControlRevision+1 || got.CurrentArtifactID != "" || got.ImplementGeneration != 1 {
		t.Errorf("after update: %+v", got)
	}
	if !d.SupersedeOpenActions || len(d.Actions) != 1 || d.Actions[0].EffectKey() != "implement|task_1|2|-|1" {
		t.Errorf("update must supersede old work and plan the new version: %+v", d)
	}
}

func TestArtifactDerivesAcceptance(t *testing.T) {
	task := taskIn(protocol.TaskRunning)
	d := ArtifactSubmitted(task, implementRun(task), "art_9")
	if d.Task.CurrentArtifactID != "art_9" || len(d.Actions) != 1 || d.Actions[0].EffectKey() != "accept|task_1|1|art_9|1" {
		t.Errorf("artifact: %+v", d)
	}

	old := implementRun(task)
	old.Generation = 0
	if d := ArtifactSubmitted(task, old, "art_old"); d.Task != task || len(d.Actions) != 0 || d.Events[0].Body["stale"] != true {
		t.Errorf("artifact from an old generation must only be recorded: %+v", d)
	}
}

func TestChangesRequiredRespectsBudget(t *testing.T) {
	task := taskIn(protocol.TaskAwaitingAcceptance)
	d, err := AcceptanceSubmitted(task, verdictOn(task, protocol.VerdictChangesRequired))
	if err != nil {
		t.Fatal(err)
	}
	if d.Task.ImplementGeneration != 2 || len(d.Actions) != 1 || d.Actions[0].EffectKey() != "implement|task_1|1|-|2" {
		t.Errorf("within budget: %+v", d)
	}

	task.ImplementGeneration = task.Budget
	d, err = AcceptanceSubmitted(task, verdictOn(task, protocol.VerdictChangesRequired))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Actions) != 0 || d.Task.Block != protocol.BlockBudgetExhausted || len(d.OpenDecisions) != 1 {
		t.Errorf("budget exhausted: %+v", d)
	}
}

func TestStaleAcceptanceDoesNotComplete(t *testing.T) {
	task := taskIn(protocol.TaskAwaitingAcceptance)
	verdict := verdictOn(task, protocol.VerdictPassed)
	updated, err := UpdateSpec(task, changedSpec(), execSpec, task.ControlRevision)
	if err != nil {
		t.Fatal(err)
	}
	d, err := AcceptanceSubmitted(updated.Task, verdict)
	if err != nil {
		t.Fatal(err)
	}
	if d.Task.Lifecycle == protocol.TaskCompleted || d.Events[0].Body["stale"] != true {
		t.Errorf("a pass for an older spec version closed the task: %+v", d)
	}
}

func TestAcceptanceEnvFailureBlocks(t *testing.T) {
	for _, v := range []protocol.Verdict{protocol.VerdictBlocked, protocol.VerdictInconclusive} {
		task := taskIn(protocol.TaskAwaitingAcceptance)
		d, err := AcceptanceSubmitted(task, verdictOn(task, v))
		if err != nil {
			t.Fatal(err)
		}
		if d.Task.Lifecycle != protocol.TaskAwaitingAcceptance || d.Task.Block != protocol.BlockAcceptanceEnvUnavailable || len(d.Actions) != 0 {
			t.Errorf("%s: %+v", v, d)
		}
	}
}

func TestOnlyAcceptanceRunsJudge(t *testing.T) {
	task := taskIn(protocol.TaskAwaitingAcceptance)
	fact := verdictOn(task, protocol.VerdictPassed)
	fact.Role = protocol.RoleImplement
	if _, err := AcceptanceSubmitted(task, fact); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Errorf("implement run submitting a verdict: err = %v, want forbidden", err)
	}
}

func TestRunTransitions(t *testing.T) {
	tests := []struct {
		from, to    protocol.RunObserved
		reconciling bool
		want        bool
	}{
		{protocol.RunStarting, protocol.RunRunning, false, true},
		{protocol.RunRunning, protocol.RunWaiting, false, true},
		{protocol.RunWaiting, protocol.RunRunning, false, true},
		{protocol.RunRunning, protocol.RunFinished, false, true},
		{protocol.RunRunning, protocol.RunUnknown, false, true},
		{protocol.RunFinished, protocol.RunRunning, false, false},
		{protocol.RunFailed, protocol.RunRunning, true, false},
		{protocol.RunUnknown, protocol.RunFinished, false, false},
		{protocol.RunUnknown, protocol.RunFinished, true, true},
		{protocol.RunUnknown, protocol.RunUnknown, true, false},
		{protocol.RunStarting, protocol.RunFinished, false, false},
		{protocol.RunRunning, protocol.RunRunning, false, false},
		{"bogus", protocol.RunRunning, false, false},
	}
	for _, tt := range tests {
		if got := RunTransitionAllowed(tt.from, tt.to, tt.reconciling); got != tt.want {
			t.Errorf("RunTransitionAllowed(%s, %s, %v) = %v, want %v", tt.from, tt.to, tt.reconciling, got, tt.want)
		}
	}
}

// TestInvariantsUnderRandomCommands drives tasks through random sequences of
// commands and facts, including stale and forged ones, and checks that a
// task only completes on a current passing verdict from an acceptance run and
// that no business effect is ever derived twice.
func TestInvariantsUnderRandomCommands(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	reached := map[string]int{}
	for iter := range 2000 {
		// A small budget lets the walk exhaust it within one sequence.
		spec := execSpec
		spec.MaxImplementAttempts = 2
		d, err := NewTask("task_r", "p", spec, 0)
		if err != nil {
			t.Fatal(err)
		}
		task := d.Task
		seen := map[string]bool{}
		latestAccept := map[string]int64{}
		record := func(d Decision) {
			for _, a := range d.Actions {
				if seen[a.EffectKey()] {
					t.Fatalf("iteration %d: effect %s derived twice", iter, a.EffectKey())
				}
				seen[a.EffectKey()] = true
				if a.Kind == protocol.ActionAccept {
					latestAccept[a.ArtifactID] = max(latestAccept[a.ArtifactID], a.Generation)
				}
			}
		}
		record(d)
		artifacts := 0
		for range 80 {
			before := task
			var next Decision
			var err error
			// Most facts are current; the rest are off by one in either
			// direction to exercise stale handling.
			fuzz := func(n int64) int64 {
				if rng.IntN(10) < 7 {
					return n
				}
				return n + int64(rng.IntN(2))*2 - 1
			}
			switch rng.IntN(9) {
			case 0:
				s := spec
				s.Constraints = []string{string(rune('a' + rng.IntN(3)))}
				next, err = UpdateSpec(task, s, spec, task.ControlRevision)
				if err == nil {
					spec = s
				}
			case 1:
				next, err = SetPriority(task, int64(rng.IntN(5)), task.ControlRevision)
			case 2:
				r := implementRun(task)
				r.SpecVersion, r.Generation = fuzz(r.SpecVersion), fuzz(r.Generation)
				next = RunStarted(task, r)
			case 3:
				r := implementRun(task)
				r.SpecVersion, r.Generation = fuzz(r.SpecVersion), fuzz(r.Generation)
				artifacts++
				next = ArtifactSubmitted(task, r, "art_"+string(rune('a'+artifacts%26)))
			case 4, 5:
				v := []protocol.Verdict{protocol.VerdictPassed, protocol.VerdictChangesRequired, protocol.VerdictBlocked}[rng.IntN(3)]
				a := verdictOn(task, v)
				a.SpecVersion = fuzz(a.SpecVersion)
				if rng.IntN(4) == 0 {
					a.ArtifactID = "art_forged"
				}
				if rng.IntN(6) == 0 {
					a.Role = protocol.RoleImplement
				}
				next, err = AcceptanceSubmitted(task, a)
				if err == nil && next.Task.Lifecycle == protocol.TaskCompleted && before.Lifecycle != protocol.TaskCompleted {
					if a.Verdict != protocol.VerdictPassed || a.Role != protocol.RoleAccept || a.SpecVersion != before.SpecVersion ||
						a.ArtifactID != before.CurrentArtifactID || before.Lifecycle != protocol.TaskAwaitingAcceptance {
						t.Fatalf("iteration %d: completed on %+v from %+v", iter, a, before)
					}
				}
			case 7:
				r := implementRun(task)
				if rng.IntN(2) == 0 {
					r = RunFact{RunID: "run_a", Role: protocol.RoleAccept, SpecVersion: task.SpecVersion, ArtifactID: task.CurrentArtifactID}
				}
				r.SpecVersion, r.Generation = fuzz(r.SpecVersion), fuzz(r.Generation)
				next = RunFailed(task, r, "crash")
			case 8:
				// The owner answers an open decision with a retry.
				switch task.Block {
				case protocol.BlockWaitingDecision, protocol.BlockBudgetExhausted:
					next, err = RequestImplement(task, task.ImplementGeneration+1, task.ControlRevision)
				case protocol.BlockAcceptanceEnvUnavailable:
					latest := latestAccept[task.CurrentArtifactID]
					next, err = RequestAcceptance(task, latest, latest+1, task.ControlRevision)
				default:
					next = Decision{Task: task}
				}
			case 6:
				if rng.IntN(60) == 0 {
					next, err = Cancel(task, task.ControlRevision)
				} else {
					next = Decision{Task: task}
				}
			}
			if err != nil {
				continue
			}
			record(next)
			if next.Task.Lifecycle != before.Lifecycle {
				reached[string(next.Task.Lifecycle)]++
			}
			if next.Task.Block == protocol.BlockBudgetExhausted && before.Block != protocol.BlockBudgetExhausted {
				reached["budget_exhausted"]++
			}
			task = next.Task
			if before.Lifecycle.Terminal() && task.Lifecycle != before.Lifecycle {
				t.Fatalf("iteration %d: left terminal stage %s for %s", iter, before.Lifecycle, task.Lifecycle)
			}
		}
	}
	// Guard against a vacuous run: the random walk must actually reach the
	// outcomes whose invariants it checks.
	for _, outcome := range []string{"completed", "changes_required", "cancelled", "budget_exhausted"} {
		if reached[outcome] == 0 {
			t.Errorf("random walk never reached %s; coverage: %v", outcome, reached)
		}
	}
	t.Logf("coverage: %v", reached)
}

func TestRequestImplement(t *testing.T) {
	task := taskIn(protocol.TaskChangesRequired)
	task.Block = protocol.BlockBudgetExhausted
	task.ImplementGeneration, task.Budget = 3, 3

	d, err := RequestImplement(task, 4, task.ControlRevision)
	if err != nil {
		t.Fatal(err)
	}
	if d.Task.ImplementGeneration != 4 || d.Task.Block != protocol.BlockNone || d.Task.Budget != 4 ||
		len(d.Actions) != 1 || d.Actions[0].EffectKey() != "implement|task_1|1|-|4" {
		t.Errorf("explicit retry beyond budget: %+v", d)
	}

	replay, err := RequestImplement(d.Task, 4, task.ControlRevision)
	if err != nil || replay.Task != d.Task || replay.Actions[0].EffectKey() != "implement|task_1|1|-|4" {
		t.Errorf("replaying an existing generation must return its effect unchanged: %+v, %v", replay, err)
	}

	if _, err := RequestImplement(d.Task, 6, d.Task.ControlRevision); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Errorf("skipping a generation: err = %v", err)
	}
	if _, err := RequestImplement(taskIn(protocol.TaskRunning), 2, 5); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Errorf("retry while running: err = %v", err)
	}
}

func TestRunFailed(t *testing.T) {
	running := taskIn(protocol.TaskRunning)
	d := RunFailed(running, implementRun(running), "no_changes")
	if d.Task.Lifecycle != protocol.TaskQueued || d.Task.Block != protocol.BlockWaitingDecision || len(d.OpenDecisions) != 1 || len(d.Actions) != 0 {
		t.Errorf("implement failure: %+v", d)
	}
	// After the failure, another generation is an explicit request.
	if _, err := RequestImplement(d.Task, d.Task.ImplementGeneration+1, d.Task.ControlRevision); err != nil {
		t.Errorf("retry after failure: %v", err)
	}

	old := implementRun(running)
	old.Generation--
	if d := RunFailed(running, old, "crash"); d.Task != running || len(d.OpenDecisions) != 0 || d.Events[0].Body["stale"] != true {
		t.Errorf("old generation failure moved the task: %+v", d)
	}

	waiting := taskIn(protocol.TaskAwaitingAcceptance)
	acc := RunFact{RunID: "run_a", Role: protocol.RoleAccept, SpecVersion: waiting.SpecVersion, ArtifactID: waiting.CurrentArtifactID}
	d = RunFailed(waiting, acc, "sandbox crashed")
	if d.Task.Lifecycle != protocol.TaskAwaitingAcceptance || d.Task.Block != protocol.BlockAcceptanceEnvUnavailable || len(d.OpenDecisions) != 1 {
		t.Errorf("acceptance failure: %+v", d)
	}
	acc.ArtifactID = "art_old"
	if d := RunFailed(waiting, acc, "x"); d.Task != waiting {
		t.Errorf("failure of an old acceptance moved the task: %+v", d)
	}
}

func TestRequestAcceptance(t *testing.T) {
	blocked := taskIn(protocol.TaskAwaitingAcceptance)
	blocked.Block = protocol.BlockAcceptanceEnvUnavailable

	d, err := RequestAcceptance(blocked, 1, 2, blocked.ControlRevision)
	if err != nil || d.Task.Block != protocol.BlockNone || len(d.Actions) != 1 || d.Actions[0].EffectKey() != "accept|task_1|1|art_1|2" {
		t.Fatalf("retry acceptance: %+v, %v", d, err)
	}
	if d, err := RequestAcceptance(blocked, 2, 2, 0); err != nil || d.Actions[0].Generation != 2 || d.Task != blocked {
		t.Errorf("replayed generation: %+v, %v", d, err)
	}
	cases := []struct {
		name string
		task Task
		gen  int64
		code protocol.Code
	}{
		{"not blocked", taskIn(protocol.TaskAwaitingAcceptance), 2, protocol.CodeInvalidTransition},
		{"skips a generation", blocked, 3, protocol.CodeInvalidRequest},
		{"not awaiting acceptance", taskIn(protocol.TaskRunning), 2, protocol.CodeInvalidTransition},
	}
	for _, tc := range cases {
		if _, err := RequestAcceptance(tc.task, 1, tc.gen, tc.task.ControlRevision); protocol.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.code)
		}
	}
}

func TestDecisionEffectsNameOptions(t *testing.T) {
	for _, d := range []DecisionDraft{failedImplementDecision("q"), budgetDecision("q"), acceptanceDecision("q")} {
		for option := range d.Effects {
			if !slices.Contains(d.Options, option) {
				t.Errorf("effect on %q, which is not an option of %v", option, d.Options)
			}
		}
		if d.Effects[optionCancel] != protocol.EffectCancel {
			t.Errorf("%v: cancel has effect %q", d.Options, d.Effects[optionCancel])
		}
		for _, judgment := range []string{optionReviseSpec, optionFixAcceptEnv} {
			if _, ok := d.Effects[judgment]; ok {
				t.Errorf("%q has an effect; it needs the agent", judgment)
			}
		}
	}
}

func TestCancelClearsTheBlock(t *testing.T) {
	task := taskIn(protocol.TaskQueued)
	task.Block = protocol.BlockWaitingDecision
	d, err := Cancel(task, task.ControlRevision)
	if err != nil || d.Task.Lifecycle != protocol.TaskCancelled || d.Task.Block != protocol.BlockNone {
		t.Errorf("Cancel = %+v, %v; want cancelled and not blocked", d.Task, err)
	}
}
