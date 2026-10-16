package main

import (
	"bytes"
	"os"
	"testing"
)

// committed is the schema checked into the repository.
const committed = "../generated/api.schema.json"

func TestCommittedSchemaIsCurrent(t *testing.T) {
	want, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(committed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is out of date; run: go run ./internal/protocol/schemagen", committed)
	}
}
