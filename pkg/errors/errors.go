// Package errors provides the typed error model shared by every layer of the
// mod manager. Errors carry a stable machine readable Code, a human readable
// Message and an optional wrapped Cause, so that the CLI, the Wails bindings and
// external Go consumers can all classify a failure without string matching.
package errors

import (
	"errors"
	"fmt"
	"strings"
)

// Code is a stable, machine readable error identifier. Codes are part of the
// public contract: they may gain new members but existing values never change.
type Code string

const (
	CodeUnknown Code = "UNKNOWN"

	// Configuration & startup.
	ErrCodeConfigInvalid Code = "CONFIG_INVALID"
	ErrCodeConfigIO      Code = "CONFIG_IO"
	ErrCodeNotFound      Code = "NOT_FOUND"

	// Arguments a caller got wrong, as opposed to state that is wrong.
	ErrCodeInvalidArgument Code = "INVALID_ARGUMENT"

	// Mods.
	ErrCodeModNotFound       Code = "MOD_NOT_FOUND"
	ErrCodeModAlreadyExists  Code = "MOD_ALREADY_EXISTS"
	ErrCodeModNotInstalled   Code = "MOD_NOT_INSTALLED"
	ErrCodeInvalidMod        Code = "INVALID_MOD"
	ErrCodeDependencyMissing Code = "DEPENDENCY_MISSING"
	ErrCodeDependencyCycle   Code = "DEPENDENCY_CYCLE"
	ErrCodeHookFailed        Code = "HOOK_FAILED"
	ErrCodeHookTimeout       Code = "HOOK_TIMEOUT"
	ErrCodeHookInvalid       Code = "HOOK_INVALID"
	ErrCodeInstallFailed     Code = "INSTALL_FAILED"
	ErrCodeUninstallFailed   Code = "UNINSTALL_FAILED"
	ErrCodeOperationCanceled Code = "OPERATION_CANCELED"
	ErrCodeNoStoragePath     Code = "NO_STORAGE_PATH"

	// Game.
	ErrCodeGameNotFound   Code = "GAME_NOT_FOUND"
	ErrCodeGameInvalid    Code = "GAME_INVALID"
	ErrCodeModsPathNotDir Code = "MODS_PATH_NOT_DIR"

	// Sources.
	ErrCodeSourceUnavailable Code = "SOURCE_UNAVAILABLE"
	ErrCodeSourceNotFound    Code = "SOURCE_NOT_FOUND"
	ErrCodeSourceExists      Code = "SOURCE_EXISTS"
	ErrCodeSourceDisabled    Code = "SOURCE_DISABLED"

	// ErrCodeSourceRejected is a request the source understood and refused: a 4xx
	// that is not about the credential, the rate limit or a missing object. It is
	// deliberately distinct from ErrCodeSourceUnavailable, which means the far end
	// could not be reached or could not answer. Telling those apart is what keeps a
	// retry loop from hammering a service that is healthy and refusing everything.
	ErrCodeSourceRejected Code = "SOURCE_REJECTED"

	ErrCodeAuthFailed     Code = "AUTH_FAILED"
	ErrCodeAuthRequired   Code = "AUTH_REQUIRED"
	ErrCodeDownloadFailed Code = "DOWNLOAD_FAILED"
	ErrCodeSearchFailed   Code = "SEARCH_FAILED"
	ErrCodeIntegrity      Code = "INTEGRITY_CHECK_FAILED"

	// Cache.
	ErrCodeCacheMiss    Code = "CACHE_MISS"
	ErrCodeCacheCorrupt Code = "CACHE_CORRUPT"

	// Plugins.
	ErrCodeRunnerNotFound Code = "RUNNER_NOT_FOUND"
	// ErrCodeHookDenied is a hook script reaching for a capability its
	// permissions do not grant. It is distinct from ErrCodeHookInvalid (the
	// declaration itself is fine) and from ErrCodeHookFailed (the script ran,
	// but refused): a denied capability is a policy outcome, and a mod author
	// debugging one needs to see exactly that.
	ErrCodeHookDenied Code = "HOOK_DENIED"
	ErrCodeDuplicate  Code = "DUPLICATE"

	// Game actions.
	//
	// An action failing to *resolve* is an argument mistake and is reported as
	// ErrCodeInvalidArgument; these three describe a real action that could not
	// be looked up, is not offered on this platform, or was found and then
	// failed. Splitting them is what lets a frontend disable the right button
	// (unavailable) instead of showing an error after the user pressed it.
	ErrCodeActionNotFound    Code = "ACTION_NOT_FOUND"
	ErrCodeActionUnavailable Code = "ACTION_UNAVAILABLE"
	ErrCodeActionFailed      Code = "ACTION_FAILED"

	// Cloud sharing.
	ErrCodeCloudProviderNotFound Code = "CLOUD_PROVIDER_NOT_FOUND"
	ErrCodeCloudAuthRequired     Code = "CLOUD_AUTH_REQUIRED"
	ErrCodeShareExpired          Code = "SHARE_EXPIRED"
	ErrCodeShareInvalid          Code = "SHARE_INVALID"
	ErrCodeShareRevoked          Code = "SHARE_REVOKED"
	ErrCodeGameNotRegistered     Code = "GAME_NOT_REGISTERED"
	ErrCodeFileTooLarge          Code = "FILE_TOO_LARGE"
	ErrCodeIntegrityMismatch     Code = "INTEGRITY_MISMATCH"
	ErrCodeAllProvidersFailed    Code = "ALL_PROVIDERS_FAILED"
	ErrCodeShareNotFound         Code = "SHARE_NOT_FOUND"
	ErrCodeCloudUploadFailed     Code = "CLOUD_UPLOAD_FAILED"
	ErrCodeCloudDownloadFailed   Code = "CLOUD_DOWNLOAD_FAILED"
)

// AppError is the concrete error type returned by every service. The zero value
// is not useful; construct one with New, Wrap or one of the sentinel helpers.
type AppError struct {
	Cause   error  `json:"-"`
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

var _ error = (*AppError)(nil)

// New builds an AppError with a formatted message.
func New(code Code, format string, args ...any) *AppError {
	return &AppError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap annotates err with a code and message. It returns nil when err is nil so
// it can be used inline on a call's return value.
func Wrap(err error, code Code, format string, args ...any) error {
	if err == nil {
		return nil
	}

	return &AppError{Code: code, Message: fmt.Sprintf(format, args...), Cause: err}
}

// Annotate adds context to err the way Wrap does, except that it does not restate
// a code err already carries.
//
// Wrapping an error with the code it already has prints that code twice —
// "GAME_NOT_FOUND: could not locate an installation: GAME_NOT_FOUND: none found" —
// which reads as two separate failures where there is one. In that case only the
// inner *message* is borrowed. The cause is still attached, so errors.Is keeps
// finding the original error and a caller can still walk the chain.
func Annotate(err error, code Code, format string, args ...any) error {
	if err == nil {
		return nil
	}

	if IsAppError(err, code) {
		return &AppError{
			Code:    code,
			Message: fmt.Sprintf(format, args...) + ": " + Message(err),
			Cause:   causeMessage{err: err},
		}
	}

	return Wrap(err, code, format, args...)
}

// causeMessage renders an error as its message alone, dropping the code the
// reader has already been given. It still unwraps to the original error, so
// errors.Is, errors.As and Code keep seeing the whole chain.
type causeMessage struct {
	err error
}

func (c causeMessage) Error() string { return Message(c.err) }

func (c causeMessage) Unwrap() error { return c.err }

// Error implements error. The rendered text always starts with the code so that
// log output remains greppable: "MOD_NOT_FOUND: mod \"x\" is not installed".
func (e *AppError) Error() string {
	if e == nil {
		return "<nil>"
	}

	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = string(e.Code)
	}

	if e.Cause == nil {
		return fmt.Sprintf("%s: %s", e.Code, msg)
	}
	// A message that already ends with the cause's own rendering must not have
	// it appended again. Wrapping with `"do %s", err` is a natural thing to write
	// and was written in many places here; the cause is already carried, so
	// appending it a second time prints it twice and makes every such error look
	// like it is complaining twice about the same thing. Testing for the suffix
	// rather than rewriting the call sites keeps the guard honest if one slips
	// through: nothing is lost either way, because the text is still present.
	if cause := e.Cause.Error(); cause != "" && strings.HasSuffix(msg, cause) {
		return fmt.Sprintf("%s: %s", e.Code, msg)
	}

	return fmt.Sprintf("%s: %s: %s", e.Code, msg, e.Cause.Error())
}

// Unwrap exposes the wrapped cause to errors.Is and errors.As.
func (e *AppError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Cause
}

// Is lets errors.Is compare two AppErrors by code alone. Message differences are
// not part of the identity: callers branch on the code, not on the prose.
func (e *AppError) Is(target error) bool {
	if e == nil || target == nil {
		return false
	}

	var t *AppError
	if !errors.As(target, &t) {
		return false
	}

	return e.Code == t.Code
}

// CodeOf returns the code carried by err, walking the wrap chain. Errors that
// are not part of the AppError model collapse to CodeUnknown.
func CodeOf(err error) Code {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}

	return CodeUnknown
}

// IsAppError reports whether err, or any error it wraps, carries the given code.
// Because the whole chain is inspected, wrapping an error never hides a code a
// caller cares about.
func IsAppError(err error, code Code) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, &AppError{Code: code})
}

// IsAnyCode reports whether err carries at least one of the given codes.
func IsAnyCode(err error, codes ...Code) bool {
	if err == nil {
		return false
	}

	for _, c := range codes {
		if errors.Is(err, &AppError{Code: c}) {
			return true
		}
	}

	return false
}

// Message returns the message of the outermost AppError in the chain, or the
// error text when the chain has none. Useful for surfacing user facing text.
func Message(err error) string {
	if err == nil {
		return ""
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Message
	}

	return err.Error()
}

// Cause returns the deepest non-AppError error in the chain, which is usually
// the operating system error (os.ErrNotExist, syscall.EACCES, ...).
func Cause(err error) error {
	for err != nil {
		appErr, ok := err.(*AppError)
		if !ok {
			return err
		}

		next := appErr.Unwrap()
		if next == nil {
			return appErr
		}

		err = next
	}

	return nil
}

// WithMessage returns a copy of err with an outer AppError message attached,
// preserving the original code and cause. It is handy for adding context
// without losing classification.
func WithMessage(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}

	return &AppError{
		Code:    CodeOf(err),
		Message: fmt.Sprintf(format, args...),
		Cause:   err,
	}
}

// Sentinels for the most frequently branched-on failures. They are pointers so
// that errors.Is matches by code even when callers construct their own AppError
// with the same code.
var (
	ErrConfigInvalid = &AppError{Code: ErrCodeConfigInvalid, Message: "configuration is invalid"}
	ErrConfigIO      = &AppError{
		Code:    ErrCodeConfigIO,
		Message: "configuration could not be read or written",
	}
	ErrNotFound        = &AppError{Code: ErrCodeNotFound, Message: "not found"}
	ErrInvalidArgument = &AppError{Code: ErrCodeInvalidArgument, Message: "argument is invalid"}

	ErrModNotFound      = &AppError{Code: ErrCodeModNotFound, Message: "mod not found"}
	ErrModAlreadyExists = &AppError{
		Code:    ErrCodeModAlreadyExists,
		Message: "mod is already installed",
	}
	ErrModNotInstalled = &AppError{Code: ErrCodeModNotInstalled, Message: "mod is not installed"}
	ErrInvalidMod      = &AppError{
		Code:    ErrCodeInvalidMod,
		Message: "mod archive is invalid or corrupted",
	}
	ErrDependencyMissing = &AppError{
		Code:    ErrCodeDependencyMissing,
		Message: "a required dependency is missing",
	}
	ErrDependencyCycle = &AppError{
		Code:    ErrCodeDependencyCycle,
		Message: "dependency cycle detected",
	}
	ErrHookFailed  = &AppError{Code: ErrCodeHookFailed, Message: "hook script failed"}
	ErrHookTimeout = &AppError{Code: ErrCodeHookTimeout, Message: "hook script timed out"}
	ErrHookInvalid = &AppError{
		Code:    ErrCodeHookInvalid,
		Message: "hook script definition is invalid",
	}
	ErrInstallFailed   = &AppError{Code: ErrCodeInstallFailed, Message: "mod installation failed"}
	ErrUninstallFailed = &AppError{
		Code:    ErrCodeUninstallFailed,
		Message: "mod uninstallation failed",
	}
	ErrCanceled      = &AppError{Code: ErrCodeOperationCanceled, Message: "operation canceled"}
	ErrNoStoragePath = &AppError{
		Code:    ErrCodeNoStoragePath,
		Message: "no storage path configured",
	}

	ErrGameNotFound   = &AppError{Code: ErrCodeGameNotFound, Message: "game installation not found"}
	ErrGameInvalid    = &AppError{Code: ErrCodeGameInvalid, Message: "game installation is invalid"}
	ErrModsPathNotDir = &AppError{
		Code:    ErrCodeModsPathNotDir,
		Message: "mods path is not a directory",
	}

	ErrSourceUnavailable = &AppError{
		Code:    ErrCodeSourceUnavailable,
		Message: "mod source unavailable",
	}
	ErrSourceNotFound = &AppError{
		Code:    ErrCodeSourceNotFound,
		Message: "mod source not registered",
	}
	ErrSourceExists = &AppError{
		Code:    ErrCodeSourceExists,
		Message: "mod source already registered",
	}
	ErrSourceDisabled = &AppError{Code: ErrCodeSourceDisabled, Message: "mod source is disabled"}
	ErrSourceRejected = &AppError{
		Code:    ErrCodeSourceRejected,
		Message: "mod source rejected the request",
	}
	ErrAuthFailed     = &AppError{Code: ErrCodeAuthFailed, Message: "authentication failed"}
	ErrAuthRequired   = &AppError{Code: ErrCodeAuthRequired, Message: "authentication required"}
	ErrDownloadFailed = &AppError{Code: ErrCodeDownloadFailed, Message: "download failed"}
	ErrSearchFailed   = &AppError{Code: ErrCodeSearchFailed, Message: "search failed"}
	ErrIntegrity      = &AppError{Code: ErrCodeIntegrity, Message: "integrity check failed"}

	ErrCacheMiss    = &AppError{Code: ErrCodeCacheMiss, Message: "not present in cache"}
	ErrCacheCorrupt = &AppError{Code: ErrCodeCacheCorrupt, Message: "cache entry is corrupt"}

	ErrRunnerNotFound = &AppError{
		Code:    ErrCodeRunnerNotFound,
		Message: "no runner registered for hook type",
	}
	ErrDuplicate = &AppError{Code: ErrCodeDuplicate, Message: "duplicate entry"}

	ErrCloudProviderNotFound = &AppError{
		Code:    ErrCodeCloudProviderNotFound,
		Message: "cloud provider not found",
	}
	ErrCloudAuthRequired = &AppError{
		Code:    ErrCodeCloudAuthRequired,
		Message: "provider authentication required",
	}
	ErrShareExpired = &AppError{
		Code:    ErrCodeShareExpired,
		Message: "share link has expired",
	}
	ErrShareInvalid = &AppError{
		Code:    ErrCodeShareInvalid,
		Message: "share link is invalid or corrupted",
	}
	ErrShareRevoked = &AppError{
		Code:    ErrCodeShareRevoked,
		Message: "share has been revoked",
	}
	ErrGameNotRegistered = &AppError{
		Code:    ErrCodeGameNotRegistered,
		Message: "share targets a game this manager does not have",
	}
	ErrFileTooLarge = &AppError{
		Code:    ErrCodeFileTooLarge,
		Message: "file exceeds provider size limit",
	}
	ErrIntegrityMismatch = &AppError{
		Code:    ErrCodeIntegrityMismatch,
		Message: "downloaded file hash mismatch",
	}
	ErrAllProvidersFailed = &AppError{
		Code:    ErrCodeAllProvidersFailed,
		Message: "all providers failed",
	}
	ErrShareNotFound     = &AppError{Code: ErrCodeShareNotFound, Message: "share not found"}
	ErrCloudUploadFailed = &AppError{
		Code:    ErrCodeCloudUploadFailed,
		Message: "cloud upload failed",
	}
	ErrCloudDownloadFailed = &AppError{
		Code:    ErrCodeCloudDownloadFailed,
		Message: "cloud download failed",
	}
)
