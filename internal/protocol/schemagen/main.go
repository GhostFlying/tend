// Command schemagen writes the owner API's JSON Schema, from which clients in
// other languages generate their types (D22):
//
//	go run ./internal/protocol/schemagen -out internal/protocol/generated/api.schema.json
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/protocol/schema"
)

// generate returns the schema document.
func generate() ([]byte, error) {
	g := schema.New()
	for _, e := range protocol.APIEnums() {
		g.Enum(e)
	}
	for _, t := range protocol.APITypes() {
		g.Add(t)
	}
	return g.Document("TendAPI")
}

func main() {
	out := flag.String("out", "internal/protocol/generated/api.schema.json", "file to write")
	flag.Parse()
	b, err := generate()
	if err == nil {
		err = os.WriteFile(*out, b, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
