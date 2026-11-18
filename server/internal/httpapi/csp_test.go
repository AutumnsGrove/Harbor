package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

const (
	hashA = "sha256-4Ee31Y/evOyoSnpY2F8tE6I7FoaQ08zzkfHpD+Jd/s8="
	hashB = "sha256-VKi1OwhxMgXe+uuXmDNFQm8A1cGFpjp5BpBJhb2MmF4="
)

func assetsWithCSP(manifest string) fstest.MapFS {
	assets := testAssets()
	assets["csp.json"] = &fstest.MapFile{Data: []byte(manifest)}
	return assets
}

func TestCSPAllowsExactlyTheBuiltInlineScripts(t *testing.T) {
	assets := assetsWithCSP(`{"scriptHashes":["` + hashA + `","` + hashB + `"]}`)
	h := New(testConfig(t), nil, assets).Handler()

	csp := request(h, http.MethodGet, "/healthz", nil).Header().Get("Content-Security-Policy")

	for _, want := range []string{"script-src 'self' '" + hashA + "' '" + hashB + "'", "default-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP = %q, want it to contain %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP = %q, must never allow inline script", csp)
	}
}

func TestCSPWithoutAFrontendBuildHasNoHashes(t *testing.T) {
	h := New(testConfig(t), nil, testAssets()).Handler()
	csp := request(h, http.MethodGet, "/healthz", nil).Header().Get("Content-Security-Policy")

	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "sha256-") {
		t.Errorf("CSP = %q, want script-src 'self' and no hashes", csp)
	}
}

func TestCSPDropsHashesItCannotValidate(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     []string
		reject   []string
	}{
		{
			name:     "an injected directive",
			manifest: `{"scriptHashes":["sha256-abc'; script-src *"]}`,
			reject:   []string{"script-src *"},
		},
		{
			name:     "an empty hash",
			manifest: `{"scriptHashes":[""]}`,
			reject:   []string{"''"},
		},
		{
			name:     "a non-sha256 algorithm",
			manifest: `{"scriptHashes":["sha384-4Ee31Y/evOyoSnpY2F8tE6I7FoaQ08zzkfHpD+Jd/s8="]}`,
			reject:   []string{"sha384-"},
		},
		{
			name:     "a truncated digest",
			manifest: `{"scriptHashes":["sha256-4Ee31Y/evOyoSnpY2F8tE6I7FoaQ08zzkfHpD+Jd/s="]}`,
			reject:   []string{"sha256-4Ee31Y"},
		},
		{
			name:     "the valid one survives next to junk",
			manifest: `{"scriptHashes":["not a hash","` + hashA + `","` + hashA + `"]}`,
			want:     []string{"'" + hashA + "'"},
		},
		{
			name:     "not JSON at all",
			manifest: `script-src *`,
			reject:   []string{"sha256-"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := New(testConfig(t), nil, assetsWithCSP(test.manifest)).Handler()
			csp := request(h, http.MethodGet, "/healthz", nil).Header().Get("Content-Security-Policy")

			for _, want := range test.want {
				if !strings.Contains(csp, want) {
					t.Errorf("CSP = %q, want it to contain %q", csp, want)
				}
			}
			for _, reject := range test.reject {
				if strings.Contains(csp, reject) {
					t.Errorf("CSP = %q, must not contain %q", csp, reject)
				}
			}
			if !strings.Contains(csp, "script-src 'self'") {
				t.Errorf("CSP = %q, want the self source to survive every manifest", csp)
			}
		})
	}
}

func TestCSPHashCountIsBounded(t *testing.T) {
	hashes := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		// Distinct but syntactically valid hashes.
		hashes = append(hashes, "sha256-"+strings.Repeat("A", 42)+string(rune('A'+i%26))+"=")
	}
	manifest := `{"scriptHashes":["` + strings.Join(hashes, `","`) + `"]}`

	h := New(testConfig(t), nil, assetsWithCSP(manifest)).Handler()
	csp := request(h, http.MethodGet, "/healthz", nil).Header().Get("Content-Security-Policy")

	if got := strings.Count(csp, "sha256-"); got != maxScriptHashes {
		t.Errorf("CSP carries %d hashes, want at most %d", got, maxScriptHashes)
	}
}
