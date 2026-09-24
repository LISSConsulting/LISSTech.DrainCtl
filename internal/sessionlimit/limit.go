//go:build windows

package sessionlimit

// Unlimited is the configured value used by deployed RD Session Hosts to
// represent an effectively unlimited connection count. Larger values are also
// treated as unlimited because they cannot be useful dashboard capacity limits.
const Unlimited = uint64(9999)

// Normalize converts a configured session limit into a finite dashboard
// capacity. The bool is false when the value is unset or represents unlimited.
func Normalize(value uint64) (int, bool) {
	if value == 0 || value >= Unlimited {
		return 0, false
	}
	return int(value), true
}
