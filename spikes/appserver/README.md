# appserver spike

Drives `traex app-server` over stdio and checks, in one run:

- thread lifecycle and the base context size (A1)
- `thread/inject_items` between turns, during a running turn, and when the
  same item is injected twice (A2)
- manual compaction and whether injected items survive it (A3)
- client-implemented dynamic tools and the identity carried by
  `item/tool/call` (A4)
- restarting the app-server and resuming the thread (A6)

Run inside the dev container:

```sh
go run ./spikes/appserver
```

The model comes from `TEND_TRAEX_MODEL` (default `GPT-5.6-Sol`). Each run
writes a full message transcript, a results JSON and a scratch working
directory under `spikes/appserver/out/`, which is not committed. Conclusions
are recorded in `docs/findings/spike-a.md`.
