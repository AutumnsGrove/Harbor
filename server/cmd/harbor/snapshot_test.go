package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

func snapshotEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		build.EnvDevNoAuth:    "1",
		build.EnvDataDir:      t.TempDir(),
		build.EnvUpdateDir:    t.TempDir(),
		build.EnvAllowedHosts: "localhost",
	}
}

func TestSnapshotCommand(t *testing.T) {
	values := snapshotEnv(t)
	var stdout bytes.Buffer

	if err := run(context.Background(), []string{"snapshot", "--pre-update"}, env(values), &stdout); err != nil {
		t.Fatalf("run(snapshot --pre-update) error = %v", err)
	}

	dest := filepath.Join(values[build.EnvDataDir], "backups", "pre-update.db")
	if err := store.Verify(dest); err != nil {
		t.Fatalf("the pre-update snapshot is not a valid database: %v", err)
	}
	if !strings.Contains(stdout.String(), dest) {
		t.Errorf("run(snapshot) printed %q, want the destination path", stdout.String())
	}
}

func TestSnapshotCommandRefusesOtherDestinations(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no flag", args: []string{"snapshot"}},
		{name: "a caller-chosen path", args: []string{"snapshot", "/tmp/elsewhere.db"}},
		{name: "an extra flag", args: []string{"snapshot", "--pre-update", "--force"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			err := run(context.Background(), test.args, env(snapshotEnv(t)), &stdout)
			if err == nil {
				t.Fatal("run(snapshot) error = nil, want a rejection")
			}
			if !strings.Contains(err.Error(), "--pre-update") {
				t.Errorf("run(snapshot) error = %q, want it to name the flag", err)
			}
		})
	}
}

func TestSnapshotCommandFailsOnAnUnusableDataDir(t *testing.T) {
	values := snapshotEnv(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	values[build.EnvDataDir] = filepath.Join(parent, "data")

	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"snapshot", "--pre-update"}, env(values), &stdout); err == nil {
		t.Fatal("run(snapshot) on a read-only data dir error = nil, want a rejection")
	}
}
