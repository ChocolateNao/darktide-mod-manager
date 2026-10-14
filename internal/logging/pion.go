package logging

import (
	"fmt"

	"github.com/pion/logging"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
)

// This file folds a third-party library's logger into this process's logger.
//
// pion narrates its own internals — ICE candidate gathering, DTLS handshakes,
// SCTP stream teardown — in detail, and pion's own default writes all of it to
// stderr at error level. In a desktop app that is the worst of both: a wall of
// noise in a console window nobody opened, and nothing at all in the log file
// somebody would actually open when a share fails to connect.
//
// Routing pion through the same logger as everything else means a connection
// failure and the events that preceded it are in one file, at one level, with
// the same redaction rules as the rest of the program. pion's volume then costs
// the user nothing: it lands in the file sink only when the configured level
// asks for it.

// pionLogger reports pion's lines through the process-wide logger.
//
// The scope is dropped, which is pion's design rather than ours: pion asks for
// one logger per subsystem ("ice", "dtls", "sctp") and a factory is the only
// shape that can return them all. A manager log line is already labelled with
// what the manager was doing, and the ICE detail that matters lives at debug.
type pionLogger struct{}

// PionLogger returns a pion logger that writes into this process's logger.
//
// It deliberately reads the package-level logger rather than taking one as an
// argument: the whole point of the central logger is that there is exactly one,
// and an adapter that could be pointed at a second one is how a second one
// appears. A caller that has not initialised the service still gets working
// output, because zerolog's own package-level default is a usable console
// logger — a library used without the manager should still say something.
func PionLogger() logging.LeveledLogger {
	return pionLogger{}
}

func (pionLogger) Trace(msg string)                  { emit(zerolog.TraceLevel, msg, nil) }
func (pionLogger) Tracef(format string, args ...any) { emit(zerolog.TraceLevel, format, args) }
func (pionLogger) Debug(msg string)                  { emit(zerolog.DebugLevel, msg, nil) }
func (pionLogger) Debugf(format string, args ...any) { emit(zerolog.DebugLevel, format, args) }
func (pionLogger) Info(msg string)                   { emit(zerolog.InfoLevel, msg, nil) }
func (pionLogger) Infof(format string, args ...any)  { emit(zerolog.InfoLevel, format, args) }
func (pionLogger) Warn(msg string)                   { emit(zerolog.WarnLevel, msg, nil) }
func (pionLogger) Warnf(format string, args ...any)  { emit(zerolog.WarnLevel, format, args) }
func (pionLogger) Error(msg string)                  { emit(zerolog.ErrorLevel, msg, nil) }
func (pionLogger) Errorf(format string, args ...any) { emit(zerolog.ErrorLevel, format, args) }

// emit writes one line at level, formatting only when there is something to
// format. Formatting is left to zerolog's Msg rather than done here: a library
// that hands over a format string and its arguments is cheaper to render than a
// fully rendered string that has to be escaped a second time.
func emit(level zerolog.Level, msg string, args []any) {
	if args == nil {
		zlog.Logger.WithLevel(level).Msg(msg)

		return
	}

	zlog.Logger.WithLevel(level).Msg(fmt.Sprintf(msg, args...))
}
