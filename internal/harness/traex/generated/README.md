# Generated: traex app-server schema subset

`schema.json` is generated; do not edit it by hand. It holds the properties
and required fields of the app-server messages tend writes out by hand in
`../protocol.go`, extracted from traex's JSON Schema bundle. The traex
version it came from is recorded in the file.

`TestSchemaSubset` checks the hand-written types against it. To regenerate
after a traex upgrade, in the dev container:

```sh
traex app-server generate-json-schema --experimental --out /tmp/traex-schema
go test ./internal/harness/traex -run TestSchemaSubset \
  -schema-dir /tmp/traex-schema -traex-version "$(traex --version | head -1 | cut -d' ' -f2)"
```

A changed message then shows up as a diff of this file, and a removed or
newly required field as a test failure.
