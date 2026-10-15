# Generated: owner API types

Do not edit these files by hand.

- `api.schema.json` is the JSON Schema of the owner API, generated from the Go types listed in `internal/protocol/api.go`:

  ```sh
  go run ./internal/protocol/schemagen
  ```

  `make gen` runs it. `TestCommittedSchemaIsCurrent` fails when the committed file no longer matches the Go types.
