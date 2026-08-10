package bundle

import (
	"strconv"
	"strings"
)

// compareVersions compares two dotted version strings (e.g. "1.2.3" or
// "v1.2.3"). Numeric segments compare numerically, otherwise lexically; a
// shorter prefix is considered lower when all shared segments are equal.
// It returns -1, 0 or +1.
func compareVersions(a, b string) int {
	as := splitVersion(a)
	bs := splitVersion(b)
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		ai, aErr := strconv.Atoi(as[i])
		bi, bErr := strconv.Atoi(bs[i])
		switch {
		case aErr == nil && bErr == nil:
			if ai < bi {
				return -1
			}
			if ai > bi {
				return 1
			}
		default:
			if as[i] < bs[i] {
				return -1
			}
			if as[i] > bs[i] {
				return 1
			}
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}

func splitVersion(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if v == "" {
		return nil
	}
	return strings.Split(v, ".")
}
