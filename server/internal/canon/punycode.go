package canon

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// RFC 3492 constants.
const (
	base        = 36
	tMin        = 1
	tMax        = 26
	skew        = 38
	damp        = 700
	initialBias = 72
	initialN    = 128
)

// toASCIILabel converts one already-lowercased host label to its ASCII form.
// ASCII labels (including ones that already start with "xn--") pass through
// untouched, so the conversion is idempotent. This is deliberately not full
// IDNA (no NFC or UTS 46 mapping): Harbor needs a stable key, not a browser's
// address bar, and a new dependency is not worth the difference.
func toASCIILabel(label string) (string, error) {
	if isASCII(label) {
		return label, nil
	}
	encoded, err := punyEncode(label)
	if err != nil {
		return "", err
	}
	return "xn--" + encoded, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func adapt(delta, numPoints int, first bool) int {
	if first {
		delta /= damp
	} else {
		delta /= 2
	}
	delta += delta / numPoints
	k := 0
	for delta > ((base-tMin)*tMax)/2 {
		delta /= base - tMin
		k += base
	}
	return k + (base-tMin+1)*delta/(delta+skew)
}

func encodeDigit(d int) byte {
	if d < 26 {
		return byte('a' + d)
	}
	return byte('0' + d - 26)
}

func punyEncode(label string) (string, error) {
	if !utf8.ValidString(label) {
		return "", errors.New("label is not valid UTF-8")
	}
	runes := []rune(label)
	var out strings.Builder
	basic := 0
	for _, r := range runes {
		if r < initialN {
			out.WriteRune(r)
			basic++
		}
	}
	h := basic
	if basic > 0 {
		out.WriteByte('-')
	}

	n, delta, bias := initialN, 0, initialBias
	for h < len(runes) {
		m := int(^uint(0) >> 1)
		for _, r := range runes {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		if m-n > (int(^uint(0)>>1)-delta)/(h+1) {
			return "", errors.New("label overflows")
		}
		delta += (m - n) * (h + 1)
		n = m
		for _, r := range runes {
			if int(r) < n {
				delta++
			}
			if int(r) != n {
				continue
			}
			q := delta
			for k := base; ; k += base {
				t := k - bias
				if t < tMin {
					t = tMin
				} else if t > tMax {
					t = tMax
				}
				if q < t {
					break
				}
				out.WriteByte(encodeDigit(t + (q-t)%(base-t)))
				q = (q - t) / (base - t)
			}
			out.WriteByte(encodeDigit(q))
			bias = adapt(delta, h+1, h == basic)
			delta = 0
			h++
		}
		delta++
		n++
	}
	return out.String(), nil
}
