package services

import "github.com/rs/zerolog"

// nopLogger is where a service's log lines go when it was built without one; a
// nil *zerolog.Logger would panic at the first log line.
var nopLogger = zerolog.Nop()

// loggerOf normalises an optional logger dependency into a usable one.
//
// A plain *zerolog.Logger: not an interface, which would only hide that
// the caller has to produce zerolog events. Errors are deliberately never
// logged: the command layer already reports each one to the caller.
func loggerOf(logger *zerolog.Logger) *zerolog.Logger {
	if logger == nil {
		return &nopLogger
	}

	return logger
}
