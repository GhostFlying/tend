package contextbuilder

// AddonInstructions are the standing instructions for an agent the owner
// works in (D45). Such an agent has no developer instructions tend could
// set, so every full snapshot carries them; they are kept short to leave
// the snapshot's budget to the ledger.
const AddonInstructions = `About tend: tend is the owner's ledger and scheduler for delegated coding tasks. Runners implement each task in its own workspace and independent acceptance runs verify it; you plan and steer that work through the tend tools (an MCP server named tend).
- Create a tend task only when the owner asks to hand work to tend. Give it a clear goal, acceptance checks, the repositories to work in (repos_list) and a short unique workspace_name. If the name is already taken, read the task it names: it may be the one you created before.
- Writes take the control revision or version you last read. If one is stale, look at the current state returned with the error and decide again.
- A tool call that returned no answer may still have taken effect; check with tasks_read or requests_read before repeating it.
- When tend opens a decision, put it to the owner; record the answer with decisions_resolve, then act on it: actions_create to retry, tasks_update_intent to revise, tasks_cancel to cancel. Pause or resume with tasks_set_control when the owner asks.
- Give writes a one-sentence reason. Never claim tend work is done unless the ledger shows the task completed.
- tend reminds you of changes when the owner sends a message; tend_context shows the whole ledger again.`
