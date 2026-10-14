package logging

import (
	"fmt"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// This file gives the action layer the same logger as everything else.
//
// An action is the one part of the manager that runs somebody else's program on
// the user's behalf, so its output is the first thing anybody reads when a script
// misbehaves. Buffering it into the result and stopping there means it is only
// ever visible to whoever happened to be looking at the UI that triggered it —
// and gone by the next run. Putting the same lines in the log puts them next to
// the mod, the platform and the command that produced them.

// ActionLogger reports an action's output through the process-wide logger at
// debug level.
//
// Debug, not info: an action that runs correctly is the expected case, and a
// line per step of a working script would bury the failures. The lines are still
// kept, because the run that needs them is exactly the run nobody expected.
type ActionLogger struct{}

// ActionOutput returns a logger that satisfies domain.ActionLogger.
func ActionOutput() domain.ActionLogger { return ActionLogger{} }

// Printf implements domain.ActionLogger.
//
// The line is prefixed rather than passed as a structured field: a script's own
// output is a sentence, and a sentence spread across log fields is harder to read
// than one that says what produced it and then says the thing.
func (ActionLogger) Printf(format string, args ...any) {
	if len(args) > 0 {
		format = fmt.Sprintf(format, args...)
	}

	Debug().Msg("action: " + format)
}
