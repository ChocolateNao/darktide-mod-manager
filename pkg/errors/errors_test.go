package errors

import (
	"encoding/json"
	goerrors "errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFormatsMessage(t *testing.T) {
	err := New(ErrCodeModNotFound, "mod %q is not installed", "better-ui")
	assert.Equal(t, `MOD_NOT_FOUND: mod "better-ui" is not installed`, err.Error(), "Error()")

	assert.Nil(t, err.Cause, "Cause")
}

func TestErrorFallsBackToCodeWhenMessageEmpty(t *testing.T) {
	err := &AppError{Code: ErrCodeConfigInvalid}
	assert.Equal(t, "CONFIG_INVALID: CONFIG_INVALID", err.Error(), "Error()")
}

func TestErrorTrimsSurroundingWhitespace(t *testing.T) {
	err := New(CodeUnknown, "  padded  ")
	assert.Equal(t, "UNKNOWN: padded", err.Error(), "Error()")
}

func TestErrorAppendsTheCauseOnce(t *testing.T) {
	cause := goerrors.New("no such column: modsx")
	// A message that does not restate the cause gains it as a suffix.
	plain := Wrap(cause, ErrCodeConfigIO, "query mods")
	assert.Equal(t, "CONFIG_IO: query mods: no such column: modsx", plain.Error(), "Error()")
	// `Wrap(err, code, "do %s", err)` appears throughout this codebase; without the
	// guard every such error complains twice about the same thing.
	restated := Wrap(cause, ErrCodeConfigIO, "query mods: %v", cause)
	assert.Equal(
		t,
		"CONFIG_IO: query mods: no such column: modsx",
		restated.Error(),
		"restated Error()",
	)
	// The cause is still reachable either way, because Unwrap does not consult
	// the message.
	assert.True(
		t,
		goerrors.Is(restated, cause) && goerrors.Is(plain, cause),
		"the cause must stay reachable through Unwrap in both forms",
	)
	// A message that merely *mentions* the cause is not a restatement: only a
	// suffix is, so a message that puts the cause first still gets the colon.
	forward := Wrap(cause, ErrCodeConfigIO, "%v: while saving", cause)
	assert.Equal(
		t,
		"CONFIG_IO: no such column: modsx: while saving: no such column: modsx",
		forward.Error(),
		"forward Error()",
	)
	// An empty cause renders as nothing rather than as a trailing colon.
	inner := &AppError{Code: ErrCodeHookFailed, Message: "hook exited"}

	outer := Wrap(inner, ErrCodeConfigIO, "run %q", "install")
	assert.Equal(
		t,
		"CONFIG_IO: run \"install\": HOOK_FAILED: hook exited",
		outer.Error(),
		"Error()",
	)
}

func TestNilAppErrorIsSafe(t *testing.T) {
	var err *AppError
	assert.Equal(t, "<nil>", err.Error(), "nil.Error()")

	assert.Nil(t, err.Unwrap(), "nil.Unwrap() should be nil")

	assert.False(t, err.Is(ErrModNotFound), "nil.Is should be false")
}

func TestWrapNilReturnsNil(t *testing.T) {
	assert.Nil(t, Wrap(nil, ErrCodeConfigIO, "writing %s", "config"), "Wrap(nil)")
}

func TestWrapPreservesCauseChain(t *testing.T) {
	root := goerrors.New("disk on fire")
	err := Wrap(root, ErrCodeConfigIO, "saving config")

	assert.True(t, goerrors.Is(err, root), "errors.Is should find the root cause")

	assert.Equal(t, ErrCodeConfigIO, CodeOf(err), "CodeOf")

	assert.Equal(t, "CONFIG_IO: saving config: disk on fire", err.Error(), "Error()")
}

func TestIsMatchesByCodeNotPointer(t *testing.T) {
	// A freshly built error with the same code must match the sentinel, even
	// though the pointers differ and the messages differ.
	fresh := New(ErrCodeModNotFound, "totally different prose")

	assert.True(t, goerrors.Is(fresh, ErrModNotFound), "errors.Is should match by code")

	assert.False(
		t,
		goerrors.Is(New(ErrCodeGameNotFound, "x"), ErrModNotFound),
		"different codes must not match",
	)

	assert.False(t, goerrors.Is(fresh, nil), "matching against nil must be false")

	assert.False(t, ErrModNotFound.Is(nil), "Is(nil) must be false")
}

func TestIsRejectsNonAppErrorTarget(t *testing.T) {
	assert.False(
		t,
		(&AppError{Code: CodeUnknown}).Is(goerrors.New("plain")),
		"a non-AppError target must not match",
	)
}

func TestCodeOfPlainError(t *testing.T) {
	assert.Equal(t, CodeUnknown, CodeOf(goerrors.New("plain")), "CodeOf(plain)")

	assert.Equal(t, CodeUnknown, CodeOf(nil), "CodeOf(nil)")
}

func TestIsAppErrorNested(t *testing.T) {
	inner := Wrap(fs.ErrNotExist, ErrCodeGameNotFound, "scanning %s", "/games")
	outer := Wrap(inner, ErrCodeInstallFailed, "installing better-ui")

	for _, code := range []Code{ErrCodeGameNotFound, ErrCodeInstallFailed} {
		assert.True(t, IsAppError(outer, code), "IsAppError(%q) = false, want true", code)
	}

	assert.False(t, IsAppError(outer, ErrCodeHookTimeout), "unrelated code must be false")

	assert.False(t, IsAppError(nil, ErrCodeGameNotFound), "nil error must be false")
}

func TestIsAnyCode(t *testing.T) {
	err := New(ErrCodeCacheCorrupt, "bad entry")

	assert.True(
		t,
		IsAnyCode(err, ErrCodeCacheMiss, ErrCodeCacheCorrupt),
		"expected match on second code",
	)

	assert.False(t, IsAnyCode(err, ErrCodeCacheMiss), "unexpected match")

	assert.False(t, IsAnyCode(nil, ErrCodeCacheCorrupt), "nil must be false")

	assert.False(t, IsAnyCode(err), "no codes supplied must be false")
}

func TestMessageUsesOutermostAppError(t *testing.T) {
	err := Wrap(New(ErrCodeGameNotFound, "inner"), ErrCodeInstallFailed, "outer")

	assert.Equal(t, "outer", Message(err), "Message")

	assert.Equal(t, "", Message(nil), "Message(nil)")

	assert.Equal(t, fs.ErrNotExist.Error(), Message(fs.ErrNotExist), "Message(plain)")
}

func TestCauseReturnsDeepestNonAppError(t *testing.T) {
	root := fs.ErrPermission
	err := Wrap(Wrap(root, ErrCodeHookFailed, "middle"), ErrCodeInstallFailed, "outer")

	assert.Equal(t, root, Cause(err), "Cause")

	assert.Nil(t, Cause(nil), "Cause(nil)")
}

func TestCauseOfAppErrorWithoutWrap(t *testing.T) {
	err := New(CodeUnknown, "standalone")
	assert.Equal(t, error(err), Cause(err), "Cause")
}

func TestWithMessageKeepsCodeAndCause(t *testing.T) {
	root := fs.ErrNotExist
	inner := Wrap(root, ErrCodeGameNotFound, "no such path")
	outer := WithMessage(inner, "while detecting steam")

	assert.Equal(t, ErrCodeGameNotFound, CodeOf(outer), "CodeOf")

	assert.True(t, goerrors.Is(outer, root), "root cause must stay reachable")

	assert.Nil(t, WithMessage(nil, "nothing"), "WithMessage(nil) must be nil")
}

func TestSentinelsHaveUniqueCodes(t *testing.T) {
	seen := map[Code]string{}

	for name, err := range map[string]*AppError{
		"ErrConfigInvalid":     ErrConfigInvalid,
		"ErrConfigIO":          ErrConfigIO,
		"ErrNotFound":          ErrNotFound,
		"ErrModNotFound":       ErrModNotFound,
		"ErrModAlreadyExists":  ErrModAlreadyExists,
		"ErrModNotInstalled":   ErrModNotInstalled,
		"ErrInvalidMod":        ErrInvalidMod,
		"ErrDependencyMissing": ErrDependencyMissing,
		"ErrDependencyCycle":   ErrDependencyCycle,
		"ErrHookFailed":        ErrHookFailed,
		"ErrHookTimeout":       ErrHookTimeout,
		"ErrHookInvalid":       ErrHookInvalid,
		"ErrInstallFailed":     ErrInstallFailed,
		"ErrUninstallFailed":   ErrUninstallFailed,
		"ErrCanceled":          ErrCanceled,
		"ErrNoStoragePath":     ErrNoStoragePath,
		"ErrGameNotFound":      ErrGameNotFound,
		"ErrGameInvalid":       ErrGameInvalid,
		"ErrModsPathNotDir":    ErrModsPathNotDir,
		"ErrSourceUnavailable": ErrSourceUnavailable,
		"ErrSourceNotFound":    ErrSourceNotFound,
		"ErrSourceExists":      ErrSourceExists,
		"ErrSourceDisabled":    ErrSourceDisabled,
		"ErrAuthFailed":        ErrAuthFailed,
		"ErrAuthRequired":      ErrAuthRequired,
		"ErrDownloadFailed":    ErrDownloadFailed,
		"ErrSearchFailed":      ErrSearchFailed,
		"ErrIntegrity":         ErrIntegrity,
		"ErrCacheMiss":         ErrCacheMiss,
		"ErrCacheCorrupt":      ErrCacheCorrupt,
		"ErrRunnerNotFound":    ErrRunnerNotFound,
		"ErrDuplicate":         ErrDuplicate,
	} {
		require.NotNil(t, err, "%s is nil", name)

		assert.NotEmpty(t, err.Code, "%s has empty code", name)

		prev, dup := seen[err.Code]
		assert.False(t, dup, "%s and %s share code %q", name, prev, err.Code)

		seen[err.Code] = name
	}
}

func TestAppErrorJSONShape(t *testing.T) {
	// The frontend relies on {code, message} and must never receive the cause,
	// which can carry absolute paths and other local detail.
	err := Wrap(New(ErrCodeConfigInvalid, "bad"), ErrCodeConfigIO, "outer")

	data, marshalErr := json.Marshal(err)
	require.NoError(t, marshalErr, "json.Marshal")

	assert.Equal(t, `{"code":"CONFIG_IO","message":"outer"}`, string(data), "json")
}

func TestAnnotateDoesNotRepeatACode(t *testing.T) {
	inner := New(ErrCodeGameNotFound, "no installation found; set the game path in the settings")

	err := Annotate(inner, ErrCodeGameNotFound, "action %q could not locate an installation", "x:y")

	// The rendered text is what a user reads. Stating the code twice makes one
	// failure look like two.
	assert.Equal(t,
		`GAME_NOT_FOUND: action "x:y" could not locate an installation: `+
			`no installation found; set the game path in the settings`,
		err.Error(), "Error()")

	assert.Equal(t, 1, strings.Count(err.Error(), string(ErrCodeGameNotFound)), "code repetition")

	// The cause survives so callers can still branch on the original error, and
	// the inner code is still reachable through the chain.
	assert.ErrorIs(t, err, inner, "errors.Is")
	assert.True(t, IsAppError(err, ErrCodeGameNotFound), "inner code still reachable")
}

func TestAnnotateAdoptsTheCodeWhenItDiffers(t *testing.T) {
	inner := New(ErrCodeConfigIO, "cannot read the settings file")

	err := Annotate(inner, ErrCodeActionUnavailable, "action %q cannot run", "x:y")

	assert.Equal(t,
		`ACTION_UNAVAILABLE: action "x:y" cannot run: CONFIG_IO: cannot read the settings file`,
		err.Error(), "Error()")

	assert.True(t, IsAppError(err, ErrCodeActionUnavailable), "outer code")

	// IsAppError walks the whole chain, so a caller asking about the cause's
	// code still gets an answer.
	assert.True(t, IsAppError(err, ErrCodeConfigIO), "inner code")
}

func TestAnnotatePassesNilThrough(t *testing.T) {
	assert.Nil(t, Annotate(nil, ErrCodeActionFailed, "unused"), "Annotate(nil)")
}

// Annotate has to cope with an error that carries no code at all: a bare
// os.PathError from somewhere the typed model has not reached yet.
func TestAnnotateWrapsAPlainError(t *testing.T) {
	inner := goerrors.New("no such file")

	err := Annotate(inner, ErrCodeActionFailed, "action %q could not read its folder", "x:y")

	assert.Equal(t,
		`ACTION_FAILED: action "x:y" could not read its folder: no such file`,
		err.Error(), "Error()")

	assert.ErrorIs(t, err, inner, "errors.Is")
}
