//go:build windows

package updater

import (
	"fmt"
	"strconv"
	"strings"
)

// version is a parsed CalVer tag (YY.MM.N). Comparison is component-wise
// and numeric, so 26.9.21 < 26.10.5 (the literal-string comparison would
// flip that pair).
type version struct {
	year  int
	month int
	n     int
}

// parseVersion accepts both `vYY.MM.N` and `YY.MM.N`. Any other shape
// (extra components, non-numeric components, leading/trailing junk) is
// rejected with an error so the caller can fall through to the
// "older than current" handling per FR-005.
func parseVersion(s string) (version, error) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, fmt.Errorf("version: %q is not YY.MM.N", s)
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil {
		return version{}, fmt.Errorf("version: year %q: %w", parts[0], err)
	}
	month, err := strconv.Atoi(parts[1])
	if err != nil {
		return version{}, fmt.Errorf("version: month %q: %w", parts[1], err)
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil {
		return version{}, fmt.Errorf("version: n %q: %w", parts[2], err)
	}
	if year < 0 || month < 1 || month > 12 || n < 0 {
		return version{}, fmt.Errorf("version: %q has invalid component", s)
	}
	return version{year: year, month: month, n: n}, nil
}

// less reports whether a < b. Equality is "not less"; the updater treats
// "not less" as "no install needed."
func (a version) less(b version) bool {
	if a.year != b.year {
		return a.year < b.year
	}
	if a.month != b.month {
		return a.month < b.month
	}
	return a.n < b.n
}

// equals reports whether a and b are the same parsed version. This is
// the parsed-comparison primitive the replay-defense gate uses to allow
// re-polling the same legitimate release through (the "not below
// highest_seen" carve-out). Compares parsed fields rather than String()
// output so "v26.6.17" and "26.6.17" are correctly recognised as
// equal across the persisted-state and remote-fetched paths.
func (a version) equals(b version) bool {
	return a.year == b.year && a.month == b.month && a.n == b.n
}

// String renders back to canonical "YY.MM.N" form (no leading "v").
func (a version) String() string {
	return fmt.Sprintf("%d.%d.%d", a.year, a.month, a.n)
}
