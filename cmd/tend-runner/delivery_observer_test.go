package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDeliveryObserverOptInAndProviderCapabilities(t *testing.T) {
	if observer, providers, err := (deliveryObserverConfig{}).build(); err != nil || observer != nil || len(providers) != 0 {
		t.Fatalf("default=%v %v %v", observer, providers, err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("executable fixture uses Unix permissions")
	}
	bin := filepath.Join(t.TempDir(), "cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// Building capability metadata must not execute the CLI or query an account.
	c := deliveryObserverConfig{Providers: []string{"github", "codebase", "github"}, GitHubBin: bin, CodebaseBin: bin}
	observer, providers, err := c.build()
	if err != nil || observer == nil || len(providers) != 2 {
		t.Fatalf("configured=%v %v %v", observer, providers, err)
	}
	if _, _, err := (deliveryObserverConfig{Providers: []string{"github"}, GitHubBin: filepath.Join(t.TempDir(), "missing")}).build(); err == nil {
		t.Fatal("advertised unavailable CLI")
	}
	if _, _, err := (deliveryObserverConfig{Providers: []string{"future"}}).build(); err == nil {
		t.Fatal("advertised unknown provider")
	}
}
