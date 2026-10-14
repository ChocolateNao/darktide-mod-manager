package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/rs/zerolog"
)

// mask replaces the value of any sensitive field.
const mask = "[REDACTED]"

// defaultSensitive lists the field names that must never reach a log sink. It
// covers the credentials this application actually handles (source API keys,
// refresh tokens, cloud object-storage keys) plus the usual suspects a future
// call site might log.
//
// Matching is name-based and case/underscore-insensitive, so "api_key",
// "apiKey" and "API-KEY" all hit the same entry.
var defaultSensitive = []string{
	"api_key",
	"apikey",
	"apiKey",
	"token",
	"access_token",
	"refresh_token",
	"id_token",
	"access_key",
	"secret_key",
	"client_secret",
	"client_id",
	"app_key",
	"app_secret",
	"credentials",
	"credential",
	"keyring",
	"bearer",
	"password",
	"passwd",
	"secret",
	"authorization",
	"cookie",
	"session",
}

// normalizeKey folds a field name to its comparison form: lower case with
// separators removed, so casing and underscore style do not defeat the list.
func normalizeKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))

	for _, r := range key {
		switch r {
		case '_', '-', ' ', '.':
			continue
		}

		b.WriteRune(r)
	}

	return strings.ToLower(b.String())
}

// redactingWriter masks sensitive fields in a zerolog JSON line on its way to a
// sink.
//
// It sits between the level gate and the destination rather than being a
// zerolog.Hook because the event is already encoded by then; working on the
// encoded line is what lets one implementation protect every sink, including
// the GUI event emitter.
type redactingWriter struct {
	w    io.Writer
	keys map[string]struct{}
}

// NewRedactingWriter wraps w so sensitive fields are masked. extra adds field
// names on top of the built-in list; it may be nil.
func NewRedactingWriter(w io.Writer, extra []string) io.Writer {
	keys := make(map[string]struct{}, len(defaultSensitive)+len(extra))
	for _, key := range defaultSensitive {
		keys[normalizeKey(key)] = struct{}{}
	}

	for _, key := range extra {
		if key = normalizeKey(key); key != "" {
			keys[key] = struct{}{}
		}
	}

	return &redactingWriter{w: w, keys: keys}
}

// Write masks sensitive fields in p.
//
// It fails open on purpose: a line that does not parse as a JSON object is
// forwarded unchanged rather than dropped. Losing a log line is a smaller harm
// than losing the ability to debug a non-JSON sink, and redacting an unparsable
// line is guesswork.
func (r *redactingWriter) Write(p []byte) (int, error) {
	if r == nil || r.w == nil {
		return len(p), nil
	}

	body := bytes.TrimRight(p, "\n")

	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &fields); err != nil {
		_, err = r.w.Write(p)
		return len(p), err
	}

	if !r.mask(fields) {
		_, err := r.w.Write(p)
		return len(p), err
	}

	redacted, err := json.Marshal(fields)
	if err != nil {
		// Cannot happen for values that came out of json.Unmarshal, but if it
		// ever does the raw line is safer than a dropped one.
		_, err = r.w.Write(p)
		return len(p), err
	}

	redacted = append(redacted, '\n')
	_, err = r.w.Write(redacted)
	// The caller's slice is what we were asked to accept, so report its length
	// even though the bytes on the wire are a different size.
	return len(p), err
}

// mask replaces every sensitive value in fields and reports whether anything
// changed.
//
// It recurses into nested objects. A flat walk would leave every value inside
// a credential bag untouched: zerolog encodes a map passed to .Interface() as a
// nested object rather than as several top-level keys, so
// .Interface("credentials", creds) would carry a live access token past this
// writer while an equivalent .Fields(creds) would not. The masking has to
// reach where the secret can actually be.
func (r *redactingWriter) mask(fields map[string]json.RawMessage) bool {
	changed := false

	for key, value := range fields {
		if _, sensitive := r.keys[normalizeKey(key)]; sensitive {
			fields[key] = json.RawMessage(`"` + mask + `"`)
			changed = true

			continue
		}

		var nested map[string]json.RawMessage
		if err := json.Unmarshal(value, &nested); err != nil || len(nested) == 0 {
			continue
		}

		if r.mask(nested) {
			rebuilt, err := json.Marshal(nested)
			if err != nil {
				continue
			}

			fields[key] = rebuilt
			changed = true
		}
	}

	return changed
}

// HookDropBelow returns a writer that discards events below minLevel.
//
// zerolog v1.35.1 exposes no Event.SetLevel, and Event.msg runs its hooks and
// then writes unconditionally, so a hook cannot suppress anything. The gate
// below is the mechanism that actually drops events, and it is also what the
// file and console sinks use.
func HookDropBelow(minLevel zerolog.Level) io.Writer {
	return &gate{level: newAtomicLevel(minLevel)}
}
