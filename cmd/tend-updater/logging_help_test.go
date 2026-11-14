package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/logging"
)

func TestUpdaterHelpDocumentsServiceLogLevel(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), logging.EnvironmentHelp) {
		t.Fatal(output.String())
	}
}
