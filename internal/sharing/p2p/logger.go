package p2p

import (
	"github.com/pion/logging"
)

// pion expects a logger and nothing more: a factory it can ask for one logger per
// subsystem, and a level-based interface it decides what to say at which level.
//
// This process has exactly one logger (internal/logging), and the adapter that
// bridges pion's interface to it lives there, so that pion's volume — which is
// large and lands at debug — is filtered by the same configuration as everything
// else. Writing pion's output anywhere else would be the second logging path in
// the program, and the second one is how the first one stops being findable.
//
// Config.PionLogger supplies that logger. A nil one means a caller that wants no
// logging at all — a library user, or a test asserting on behaviour — and is not
// an error: the share protocol has no business logging into somebody else's log.

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

// loggerFactoryFor adapts a logger to pion's factory, substituting a logger that
// says nothing when there is none. pion logs unconditionally from paths this
// package does not control, so a nil logger has to become silence rather than a
// panic.
func loggerFactoryFor(logger logging.LeveledLogger) logging.LoggerFactory {
	if logger == nil {
		return logFactory{logger: discardLogger{}}
	}

	return logFactory{logger: logger}
}

// discardLogger is the zero-configuration default: every pion call is accepted
// and dropped. It exists so a caller that has not configured logging still gets a
// working share, and so the tests can assert on behaviour without capturing the
// noise a real connection produces.
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
