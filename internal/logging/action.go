package logging

import (
	"fmt"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// An action runs somebody else's program, so its output is the first thing anybody
// reads when a script misbehaves. The log puts it next to the mod, the platform and
// the command that produced it, not only in the UI view that triggered it.

// ActionLogger reports an action's output through the process-wide logger at
// debug level.
//
// Debug, not info: a line per step of a working script would bury the failures.
type ActionLogger struct{}

// ActionOutput returns a logger that satisfies domain.ActionLogger.
func ActionOutput() domain.ActionLogger { return ActionLogger{} }

// Logf implements domain.ActionLogger.
//
// The line is prefixed: a script's own output is a sentence, and it reads
// better whole.
func (ActionLogger) Logf(format string, args ...any) {
	if len(args) > 0 {
		format = fmt.Sprintf(format, args...)
	}

	Debug().Msg("action: " + format)
}
