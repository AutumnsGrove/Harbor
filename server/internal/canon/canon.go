// Package canon decides what "the same page" means. A re-save of a URL that
// canonicalises to an existing key is a merge, not a duplicate (design.md §5),
// so every later merge, import and replay rests on this one function.
package canon

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// CanonicalURL returns the key for the page raw points at.
//
// The rules (design.md §5, plans/02-core-library.md "Build-time decisions"):
//   - the scheme must be http or https and must be present; it is never guessed
//   - credentials in the URL are refused rather than silently stored or dropped
//   - a host containing '%' (a decoded %25 or an IPv6 zone ID) is refused: it
//     cannot be written back out and read in again as the same key
//   - scheme and host are lowercased, a unicode host becomes punycode, and the
//     default port (80 for http, 443 for https) is removed
//   - the fragment is dropped, and so are utm_*, fbclid and gclid parameters;
//     every other parameter is kept, in its original order and spelling
//   - an empty path becomes "/" (the same request), but a trailing slash on a
//     longer path is kept: /a and /a/ are different resources
//   - "www." stays distinct: collapsing it merges genuinely different sites
//
// The output canonicalises to itself.
func CanonicalURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("canon: empty URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("canon: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https":
	case "":
		return "", errors.New("canon: URL has no scheme")
	default:
		return "", fmt.Errorf("canon: scheme %q is not http or https", scheme)
	}
	if u.User != nil {
		return "", errors.New("canon: URL carries credentials")
	}
	if u.Opaque != "" {
		return "", errors.New("canon: URL is opaque")
	}

	host, err := canonicalHost(u.Hostname())
	if err != nil {
		return "", err
	}
	port, err := canonicalPort(scheme, u.Port())
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	if strings.Contains(host, ":") { // IPv6 literal
		b.WriteString("[" + host + "]")
	} else {
		b.WriteString(host)
	}
	if port != "" {
		b.WriteString(":" + port)
	}
	if path := u.EscapedPath(); path == "" {
		b.WriteString("/")
	} else {
		b.WriteString(path)
	}
	if q := stripTracking(u.RawQuery); q != "" {
		b.WriteString("?" + q)
	}
	return b.String(), nil
}

func canonicalPort(scheme, port string) (string, error) {
	if port == "" {
		return "", nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return "", fmt.Errorf("canon: port %q is not valid", port)
	}
	if (scheme == "http" && n == 80) || (scheme == "https" && n == 443) {
		return "", nil
	}
	return strconv.Itoa(n), nil
}

func canonicalHost(host string) (string, error) {
	if host == "" {
		return "", errors.New("canon: URL has no host")
	}
	host = strings.ToLower(host)
	// '%' can only arrive here by url.Parse decoding %25 in the host (or an IPv6
	// zone ID). Re-emitting it unescaped would not parse back to the same key,
	// and neither is a hostname a library of web pages can mean.
	if strings.ContainsAny(host, " \t\r\n/\\?#@[]%") {
		return "", fmt.Errorf("canon: host %q is not valid", host)
	}
	labels := strings.Split(host, ".")
	for i, label := range labels {
		ascii, err := toASCIILabel(label)
		if err != nil {
			return "", fmt.Errorf("canon: host %q: %w", host, err)
		}
		labels[i] = ascii
	}
	return strings.Join(labels, "."), nil
}

// toASCIILabel converts one already-lowercased host label to its ASCII form.
// ASCII labels — including ones that already start with "xn--" — pass through
// untouched, so the conversion is idempotent and an A-label is never decoded or
// double-encoded.
//
// A non-ASCII label that starts with "xn--" is refused. idna treats any label
// with that prefix as an A-label and decodes it before re-encoding, which for
// these labels returns a different key rather than an error; the hand-rolled
// encoder used to punycode the whole label instead. Neither result is an
// address anyone can resolve, so refusing is the one option that cannot move a
// key silently.
//
// The profile is idna.Punycode: raw RFC 3492 encoding with no UTS 46 mapping,
// which is what this package did by hand before issue #20. idna.Lookup is the
// easier recommendation to justify, but it moves keys — it maps "faß" to
// "fass", fullwidth letters to ASCII, and NFC-normalises combining sequences —
// and a moved key is either a duplicate item or a silently merged one
// (design.md §5). golden_test.go is what chose the profile, by test.
func toASCIILabel(label string) (string, error) {
	if isASCII(label) {
		return label, nil
	}
	// idna would fold invalid UTF-8 into U+FFFD and refuse it as a disallowed
	// rune; the message here says what is actually wrong.
	if !utf8.ValidString(label) {
		return "", errors.New("label is not valid UTF-8")
	}
	if strings.HasPrefix(label, "xn--") {
		return "", errors.New("label starts with xn-- but is not ASCII")
	}
	return idna.Punycode.ToASCII(label)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// stripTracking removes tracking parameters without re-encoding anything else,
// so that what is kept is byte-for-byte what the user saved.
func stripTracking(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	kept := make([]string, 0, 4)
	for _, pair := range strings.Split(rawQuery, "&") {
		if pair == "" {
			continue
		}
		name, _, _ := strings.Cut(pair, "=")
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if isTracking(strings.ToLower(name)) {
			continue
		}
		kept = append(kept, pair)
	}
	return escapeQuery(strings.Join(kept, "&"))
}

// escapeQuery percent-encodes whitespace and control bytes, which url.Parse
// tolerates in a query but which would not survive being trimmed or pasted
// again: a key must read back as itself.
func escapeQuery(q string) string {
	var b strings.Builder
	for i := 0; i < len(q); i++ {
		if c := q[i]; c <= ' ' || c == 0x7f {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isTracking(name string) bool {
	return strings.HasPrefix(name, "utm_") || name == "fbclid" || name == "gclid"
}
