# tsnet spike

Joins the tailnet as an embedded tsnet node (default hostname `tend-dev`,
ephemeral), serves HTTP on :80 and, when the tailnet issues certificates,
HTTPS on :443. Every response reports the caller identity resolved through
WhoIs.

Run inside the dev container with `TS_AUTHKEY` set:

```sh
go run ./spikes/tsnet
```

Then request `http://tend-dev/` or `https://tend-dev.<tailnet>.ts.net/` from
another tailnet device. Runtime output belongs in `spikes/tsnet/out/`, which
is not committed. Conclusions are recorded in `docs/findings/spike-a.md`.
