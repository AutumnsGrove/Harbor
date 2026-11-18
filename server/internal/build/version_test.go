package build

import (
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	tests := []struct {
		name  string
		stamp string
		want  string
	}{
		{name: "unstamped", stamp: "", want: "dev"},
		{name: "stamped by the Makefile", stamp: "r412.1a2b3c4", want: "r412.1a2b3c4"},
		{name: "stamped with a dirty suffix", stamp: "r412.1a2b3c4-dirty", want: "r412.1a2b3c4-dirty"},
		{name: "a space in the stamp", stamp: "r412 1a2b3c4", want: "dev"},
		{name: "a newline in the stamp", stamp: "r412\n1a2b3c4", want: "dev"},
		{name: "a string too long for a header", stamp: strings.Repeat("a", 65), want: "dev"},
		{name: "exactly the maximum length", stamp: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version = test.stamp
			if got := Version(); got != test.want {
				t.Errorf("Version() = %q, want %q", got, test.want)
			}
		})
	}
}

// FuzzVersion holds the property that matters for a value that travels in a
// header and a JSON field: whatever the linker stamped, what comes out is safe
// to emit (design.md §3).
func FuzzVersion(f *testing.F) {
	f.Add("r1.abcdef0")
	f.Add("")
	f.Add("r1.abc def")
	f.Add(strings.Repeat("z", 4096))

	original := version
	f.Cleanup(func() { version = original })

	f.Fuzz(func(t *testing.T, stamp string) {
		version = stamp
		got := Version()
		if !versionPattern.MatchString(got) {
			t.Fatalf("Version() = %q for stamp %q; want a match for %s", got, stamp, versionPattern)
		}
		if got == "" {
			t.Fatal("Version() returned an empty string")
		}
	})
}
