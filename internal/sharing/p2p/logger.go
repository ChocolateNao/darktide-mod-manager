package p2p

import (
	"github.com/pion/logging"
)

// pion wants a logger factory: one logger per subsystem, at whatever level pion
// decides. This process has exactly one logger (internal/logging) and pion's large,
// debug-heavy output is filtered like everything else; a second logging path is how
// the first stops being findable. A nil Config.PionLogger is not an error: this
// protocol has no business logging into somebody else's log.

// logFactory hands the same logger to every subsystem pion asks for.
//
// The scope is dropped. pion's scopes name its internals ("ice", "dtls",
// "sctp"), which tell a reader who was talking only when pion is narrating
// itself; a share log line is already labelled with what the manager was doing,
// and the detail that matters while debugging a connection lives at debug.
type logFactory struct {
	logger logging.LeveledLogger
}

// NewLogger implements pion's logging.LoggerFactory.
func (f logFactory) NewLogger(string) logging.LeveledLogger {
	return f.logger
}

// loggerFactoryFor adapts a logger to pion's factory, substituting silence for
// none. pion logs unconditionally from paths this package does not control, so a
// nil logger has to become a logger that says nothing.
func loggerFactoryFor(logger logging.LeveledLogger) logging.LoggerFactory {
	if logger == nil {
		return logFactory{logger: discardLogger{}}
	}

	return logFactory{logger: logger}
}

// discardLogger is the zero-configuration default: every pion call is accepted
// and dropped. It exists so a caller that has not configured logging still gets a
// working share, and so tests can assert on behaviour without the noise a real
// connection produces.
type discardLogger struct{}

func (discardLogger) Trace(string)          {}
func (discardLogger) Tracef(string, ...any) {}
func (discardLogger) Debug(string)          {}
func (discardLogger) Debugf(string, ...any) {}
func (discardLogger) Info(string)           {}
func (discardLogger) Infof(string, ...any)  {}
func (discardLogger) Warn(string)           {}
func (discardLogger) Warnf(string, ...any)  {}
func (discardLogger) Error(string)          {}
func (discardLogger) Errorf(string, ...any) {}

var _ logging.LeveledLogger = discardLogger{}
