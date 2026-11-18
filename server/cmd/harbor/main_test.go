package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
)

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	if err := run([]string{"version"}, &stdout); err != nil {
		t.Fatalf("run(version) error = %v", err)
	}
	if got, want := strings.TrimSpace(stdout.String()), build.Version(); got != want {
		t.Errorf("run(version) printed %q, want %q", got, want)
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		var stdout bytes.Buffer
		if err := run([]string{arg}, &stdout); err != nil {
			t.Fatalf("run(%s) error = %v", arg, err)
		}
		if !strings.Contains(stdout.String(), "Usage: harbor") {
			t.Errorf("run(%s) printed %q, want the usage text", arg, stdout.String())
		}
	}
}

func TestRunRejects(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no command", args: nil, want: "no command given"},
		{name: "an unknown command", args: []string{"frobnicate"}, want: "unknown command"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			err := run(test.args, &stdout)
			if err == nil {
				t.Fatal("run() error = nil, want a rejection")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("run() error = %q, want it to mention %q", err, test.want)
			}
		})
	}
}
