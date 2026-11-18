package httpapi

import (
	"encoding/json"
	"io/fs"
	"regexp"
)

// maxScriptHashes bounds the header. A build has one or two inline scripts; a
// manifest with hundreds is a mistake or an attack, and either way it must not
// become a kilobyte of response header.
const maxScriptHashes = 16

// scriptHashPattern is the CSP grammar for a hash source: sha256- followed by
// the standard base64 of a 32-byte digest, with padding.
var scriptHashPattern = regexp.MustCompile(`^sha256-[A-Za-z0-9+/]{43}=$`)

// cspManifest is written by web/scripts/csp.mjs after `vite build`. SvelteKit
// emits one inline bootstrap script per page; the alternatives are these hashes
// or 'unsafe-inline', and design.md §3 forbids the second one.
type cspManifest struct {
	ScriptHashes []string `json:"scriptHashes"`
}

// loadScriptHashes reads and validates the build's hashes. Anything that does
// not match the grammar is dropped rather than interpolated into a header: a
// mangled manifest must narrow the policy, never widen it.
func loadScriptHashes(assets fs.FS) []string {
	data, err := fs.ReadFile(assets, "csp.json")
	if err != nil {
		return nil
	}

	var manifest cspManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil
	}

	seen := make(map[string]bool, len(manifest.ScriptHashes))
	hashes := make([]string, 0, len(manifest.ScriptHashes))
	for _, hash := range manifest.ScriptHashes {
		if len(hashes) >= maxScriptHashes {
			break
		}
		if !scriptHashPattern.MatchString(hash) || seen[hash] {
			continue
		}
		seen[hash] = true
		hashes = append(hashes, hash)
	}
	return hashes
}
