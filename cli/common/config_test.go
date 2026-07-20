package common

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindLocalConfigFile_None(t *testing.T) {
	dir := t.TempDir()
	if got := FindLocalConfigFile(dir); got != "" {
		t.Fatalf("FindLocalConfigFile(empty dir) = %q, want empty", got)
	}
}

func TestFindLocalConfigFile_NcliYaml(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "ncli.yaml")
	if err := os.WriteFile(want, []byte("store: test.db\n"), 0644); err != nil {
		t.Fatalf("failed to create fixture: %v", err)
	}
	if got := FindLocalConfigFile(dir); got != want {
		t.Fatalf("FindLocalConfigFile() = %q, want %q", got, want)
	}
}

func TestFindLocalConfigFile_RelayYamlFallback(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "relay.yaml")
	if err := os.WriteFile(want, []byte("store: test.db\n"), 0644); err != nil {
		t.Fatalf("failed to create fixture: %v", err)
	}
	if got := FindLocalConfigFile(dir); got != want {
		t.Fatalf("FindLocalConfigFile() = %q, want %q", got, want)
	}
}

func TestFindLocalConfigFile_NcliYamlWinsOverRelayYaml(t *testing.T) {
	dir := t.TempDir()
	ncliPath := filepath.Join(dir, "ncli.yaml")
	relayPath := filepath.Join(dir, "relay.yaml")
	if err := os.WriteFile(ncliPath, []byte("store: test.db\n"), 0644); err != nil {
		t.Fatalf("failed to create ncli.yaml fixture: %v", err)
	}
	if err := os.WriteFile(relayPath, []byte("store: test.db\n"), 0644); err != nil {
		t.Fatalf("failed to create relay.yaml fixture: %v", err)
	}
	if got := FindLocalConfigFile(dir); got != ncliPath {
		t.Fatalf("FindLocalConfigFile() = %q, want %q (ncli.yaml takes priority)", got, ncliPath)
	}
}

func TestFindLocalConfigFile_DirectoryNamedNcliYamlIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "ncli.yaml"), 0755); err != nil {
		t.Fatalf("failed to create directory fixture: %v", err)
	}
	if got := FindLocalConfigFile(dir); got != "" {
		t.Fatalf("FindLocalConfigFile() with a directory named ncli.yaml = %q, want empty", got)
	}
}
