# Issue owner urgent delivery

A deployment revocation entered the ordinary Issue inbox while the responsible Agent was working. The owner did not receive it until a later Run. Main's current-turn steer already has native support, but the delivery loop excludes Issue conversations.

1. Add an explicit owner operation and CLI entry for routing an original owner message to the current Issue owner. Require the Issue revision, assignment epoch and Run identity. Bind the original source, authenticated submitting principal and exact admitted native execution. Main uses its existing delegated owner interface with the original admitted owner source. An operator uses the authenticated owner interface and the same scoped source. Preserve the original author and never give the routed notice independent owner authority.
2. Keep a separate durable queue for urgent notices within the exact live turn. A revocation references the older owner source in the same Issue and has priority over pending urgent notices. Suppress pending urgent delivery of that older source, retain its originals and already committed outcomes, and leave existing business-control operations authoritative. No new turn, process interrupt, automatic resend or implicit normal-queue fallback.
3. Reuse the existing native turn fence and completed user-item event. Record dispatch intent and exact compiled input before invoking the executor. Distinguish queued, sending, accepted, consumed, unsupported, rejected and unknown. Match consumption against the original thread, turn, input and item. Expose the status on the original notice detail and in the Issue conversation. Keep original identity and evidence through response loss, native rejection, run replacement and recovery.
4. Verify the owner operation through the real admission ledger and adapter, including source and scope denials, revocation priority, exact native consumption, negative receipts, duplicate submissions and UI labels. Confirm an ordinary injection continues through its original path. Use an isolated execution for tests.

## Calling and reading the operation

Use the Issue revision and current owner Run read from `tend issue show`. The
source is a human-authored original already linked to that Issue. Main must
also have that original admitted to its own execution. The authenticated
operator and the original owner remain separate in the receipt.

```sh
tend conversation steer-owner <issue-id> --revision <issue-revision> --file input.json --dry-run
tend message original <notice-message-id> --json
```

The JSON file supplies `run_id`, `expected_assignment_epoch`,
`source_message_id` and `expected_source_revision`. To withdraw an earlier
source's urgent routing, add `revokes_source_message_id` with its exact
original ID. Keep the notice `message_id` before submitting. A caller can
read the original notice on an interrupted response. An independently
queued ordinary original and the formal operation that revokes a business
permission keep their own identities. An executor can consume a steer only
at its safe input boundary. The receipt retains the native request
identity and the matching user item when available.

## Independent review follow-up

1. Apply the exact source and turn checks in the transaction where the runner first claims a pending native request. Keep the original request and record a definite rejection when the fence closes before that claim. A started request retains its original reconciliation path.
2. Record a positive native reply for the original request even after a caller timeout. A live matching turn returns to accepted. An ended turn retains an unknown consumption result and the confirmed acceptance.
3. Accept only a complete nonempty text source. Return a clear error before queueing a source whose image or attachment would be omitted.
4. Display the stored sender and steer receipt directly in the Issue activity and transcript. Recheck the reviewer’s native claim and late reply cases, the server activity projection and both browser widths.
