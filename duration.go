//go:build windows

package drainctl

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration that JSON-marshals as a Go duration string
// ("24h", "6h30m") rather than as nanoseconds. Operator-facing config
// fields use this type so config.json reads naturally; programmatic
// callers convert via the embedded time.Duration value.
//
// Zero value is the zero duration. UnmarshalJSON treats `null` and an
// empty string as zero so missing fields stay at their package default
// (set by Validate, not by the unmarshaler).
type Duration time.Duration

// MarshalJSON renders as a Go-duration string. The zero value renders as
// `"0s"` rather than `null` so a round-trip through MarshalJSON +
// UnmarshalJSON is stable.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts a JSON string in Go-duration form. `null` and
// an empty string yield the zero value (callers should defaults-fill
// in their Validate method). Anything else MUST be a valid duration
// per time.ParseDuration; an invalid value returns an error so the
// surrounding LoadConfig surfaces the parse failure clearly.
func (d *Duration) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*d = 0
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("duration: not a JSON string: %w", err)
	}
	if s == "" {
		*d = 0
		return nil
	}
	td, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration: parse %q: %w", s, err)
	}
	*d = Duration(td)
	return nil
}
