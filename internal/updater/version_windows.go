//go:build windows

package updater

import (
	"fmt"
	"strconv"
	"strings"
)

// version is a parsed CalVer tag (YY.DOY.N). Comparison is component-wise
// and numeric, so 26.99.21 < 26.116.5 (the literal-string comparison would
// flip that pair).
type version struct {
	year int
	doy  int
	n    int
}

// parseVersion accepts both `vYY.DOY.N` and `YY.DOY.N`. Any other shape
// (extra components, non-numeric components, leading/trailing junk) is
// rejected with an error so the caller can fall through to the
// "older than current" handling per FR-005.
func parseVersion(s string) (version, error) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, fmt.Errorf("version: %q is not YY.DOY.N", s)
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil {
		return version{}, fmt.Errorf("version: year %q: %w", parts[0], err)
	}
	doy, err := strconv.Atoi(parts[1])
	if err != nil {
		return version{}, fmt.Errorf("version: doy %q: %w", parts[1], err)
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil {
		return version{}, fmt.Errorf("version: n %q: %w", parts[2], err)
	}
	if year < 0 || doy < 0 || n < 0 {
		return version{}, fmt.Errorf("version: %q has negative component", s)
	}
	return version{year: year, doy: doy, n: n}, nil
}

// less reports whether a < b. Equality is "not less"; the updater treats
// "not less" as "no install needed."
func (a version) less(b version) bool {
	if a.year != b.year {
		return a.year < b.year
	}
	if a.doy != b.doy {
		return a.doy < b.doy
	}
	return a.n < b.n
}

// String renders back to canonical "YY.DOY.N" form (no leading "v").
func (a version) String() string {
	return fmt.Sprintf("%d.%d.%d", a.year, a.doy, a.n)
}
