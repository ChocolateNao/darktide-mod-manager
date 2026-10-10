package domain

import (
	"encoding/json"
	"time"
)

// Duration is a time.Duration that marshals to and from JSON as a Go duration
// string ("10s", "1m30s") instead of nanoseconds. The configuration file is
// hand-editable, and `"default_timeout": 10000000000` is not a value a person
// can write or read; `"10s"` is.
type Duration time.Duration

// Duration returns the value as a standard library duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// DurationOf converts a standard library duration to a config Duration.
func DurationOf(d time.Duration) Duration { return Duration(d) }

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements json.Unmarshaler. Only duration strings are
// accepted; a bare number is rejected rather than silently reinterpreted,
// because nanoseconds and seconds differ by nine zeroes and guessing wrong
// turns "30" into a third of a microsecond.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}

	*d = Duration(parsed)

	return nil
}
