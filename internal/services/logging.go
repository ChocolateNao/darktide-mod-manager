package services

import "github.com/rs/zerolog"

// nopLogger is the destination a service falls back to when it was built
// without one. Tests and small tools construct services directly, and a nil
// *zerolog.Logger would panic at the first log line, so "no logging configured"
// has to mean "logging discarded" rather than "logging fatal".
var nopLogger = zerolog.Nop()

// loggerOf normalises an optional logger dependency into a usable one.
//
// Services take a plain *zerolog.Logger rather than an interface: the log record
// type is the currency here, and an interface would only hide that the caller
// has to produce zerolog events.
//
// A note on severity. Services log debug for the steps of an operation and info
// for a state change that completed. They deliberately do not log errors: the
// command layer already reports every failure through the caller's own error
// (and the GUI shows it to the user), so a second error line per failure would
// double-count failures in the log and make "how many errors are in this file"
// an unanswerable question.
func loggerOf(logger *zerolog.Logger) *zerolog.Logger {
	if logger == nil {
		return &nopLogger
	}

	return logger
}
