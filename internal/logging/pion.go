package logging

import (
	"fmt"

	"github.com/pion/logging"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
)

// pion narrates its own internals — ICE gathering, DTLS handshakes, SCTP teardown —
// at error level on stderr, which a desktop app cannot act on. Routing it through this
// process's logger keeps it in one file at one level under the same redaction rules.

// pionLogger reports pion's lines through the process-wide logger.
//
// The scope is dropped, which is pion's design: pion asks for
// one logger per subsystem and a factory is the only shape that returns them all.
type pionLogger struct{}

// PionLogger returns a pion logger that writes into this process's logger.
//
// It reads the package-level logger: an adapter that could be pointed at a
// second logger is how a second instance would appear. A caller that never
// initialised the service still gets working output.
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

// emit writes one line at level. Formatting is left to zerolog's Msg rather
// than done here: a library handing over a format string and its arguments is
// cheaper to render than a fully rendered string escaped a second time.
func emit(level zerolog.Level, msg string, args []any) {
	if args == nil {
		zlog.Logger.WithLevel(level).Str("app", appName).Str("op", "p2p-share").Msg(msg)

		return
	}

	zlog.Logger.WithLevel(level).Str("app", appName).Str("op", "p2p-share").Msg(fmt.Sprintf(msg, args...))
}
