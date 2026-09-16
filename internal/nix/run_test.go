package nix

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRunScriptQuotedPathWithSpaces(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "my project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(projectDir, "run me.sh")
	body := "#!/bin/sh\ntouch \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(projectDir, "marker file")
	cmdWithArgs := []string{strconv.Quote(script), strconv.Quote(marker)}
	if err := RunScript(t.Context(), projectDir, cmdWithArgs, map[string]string{}); err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("script did not create %q: %v", marker, err)
	}
}
