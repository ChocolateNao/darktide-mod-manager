package api

import (
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// AppError is the concrete error type every operation in this package returns.
// The codes and helpers live in pkg/errors, re-exported here so a consumer needs
// one import. Functions rather than `var` aliases so godoc renders documentation
// next to each one.
type AppError = appErrors.AppError

// Code is the stable, machine-readable classification of a failure — for
// example "MOD_NOT_FOUND". The string values are part of this package's
// contract and do not change within a major version.
type Code = appErrors.Code

// The error codes this package's operations can produce. They are grouped the
// way the operations are: configuration, mod lifecycle, game, sources, cache.
const (
	// CodeUnknown is what CodeOf reports for an error that did not come from this
	// package at all. It is a value rather than a zero Code so a caller can never
	// mistake "not our error" for one of our codes.
	CodeUnknown Code = appErrors.CodeUnknown

	// Configuration.
	ErrCodeConfigInvalid   Code = appErrors.ErrCodeConfigInvalid
	ErrCodeConfigIO        Code = appErrors.ErrCodeConfigIO
	ErrCodeNotFound        Code = appErrors.ErrCodeNotFound
	ErrCodeInvalidArgument Code = appErrors.ErrCodeInvalidArgument

	// Mod lifecycle.
	ErrCodeModNotFound       Code = appErrors.ErrCodeModNotFound
	ErrCodeModAlreadyExists  Code = appErrors.ErrCodeModAlreadyExists
	ErrCodeModNotInstalled   Code = appErrors.ErrCodeModNotInstalled
	ErrCodeInvalidMod        Code = appErrors.ErrCodeInvalidMod
	ErrCodeDependencyMissing Code = appErrors.ErrCodeDependencyMissing
	ErrCodeDependencyCycle   Code = appErrors.ErrCodeDependencyCycle
	ErrCodeHookFailed        Code = appErrors.ErrCodeHookFailed
	ErrCodeHookTimeout       Code = appErrors.ErrCodeHookTimeout
	ErrCodeHookInvalid       Code = appErrors.ErrCodeHookInvalid
	ErrCodeHookDenied        Code = appErrors.ErrCodeHookDenied
	ErrCodeInstallFailed     Code = appErrors.ErrCodeInstallFailed
	ErrCodeUninstallFailed   Code = appErrors.ErrCodeUninstallFailed
	ErrCodeOperationCanceled Code = appErrors.ErrCodeOperationCanceled
	ErrCodeNoStoragePath     Code = appErrors.ErrCodeNoStoragePath

	// Game installation.
	ErrCodeGameNotFound   Code = appErrors.ErrCodeGameNotFound
	ErrCodeGameInvalid    Code = appErrors.ErrCodeGameInvalid
	ErrCodeModsPathNotDir Code = appErrors.ErrCodeModsPathNotDir

	// Sources.
	ErrCodeSourceUnavailable Code = appErrors.ErrCodeSourceUnavailable
	ErrCodeSourceNotFound    Code = appErrors.ErrCodeSourceNotFound
	ErrCodeSourceExists      Code = appErrors.ErrCodeSourceExists
	ErrCodeSourceDisabled    Code = appErrors.ErrCodeSourceDisabled
	ErrCodeAuthFailed        Code = appErrors.ErrCodeAuthFailed
	ErrCodeAuthRequired      Code = appErrors.ErrCodeAuthRequired
	ErrCodeDownloadFailed    Code = appErrors.ErrCodeDownloadFailed
	ErrCodeSearchFailed      Code = appErrors.ErrCodeSearchFailed
	ErrCodeIntegrity         Code = appErrors.ErrCodeIntegrity

	// Cache.
	ErrCodeCacheMiss    Code = appErrors.ErrCodeCacheMiss
	ErrCodeCacheCorrupt Code = appErrors.ErrCodeCacheCorrupt

	// Hook runners.
	ErrCodeRunnerNotFound Code = appErrors.ErrCodeRunnerNotFound
	ErrCodeDuplicate      Code = appErrors.ErrCodeDuplicate
)

// IsAppError reports whether err, or anything it wraps, carries the given code. Use
// it rather than errors.Is against a sentinel: the message of a failure is written
// for a person, while the code is the part with a stability guarantee.
func IsAppError(err error, code Code) bool { return appErrors.IsAppError(err, code) }

// IsAnyCode reports whether err carries any of the given codes, for the common
// case of "is this a cancellation or a not-found?".
func IsAnyCode(err error, codes ...Code) bool { return appErrors.IsAnyCode(err, codes...) }

// CodeOf returns the code carried by err, or CodeUnknown for an error that did
// not come from this package.
func CodeOf(err error) Code { return appErrors.CodeOf(err) }

// Message returns err's message without the "CODE: " prefix and without the
// cause chain, which is what a caller wants when showing a message to a person.
func Message(err error) string { return appErrors.Message(err) }

// Cause returns the error an AppError wraps, or nil when there is none. It is for a
// caller that needs to inspect, say, a net error under an InstallFailed.
func Cause(err error) error { return appErrors.Cause(err) }

// New builds an AppError. It is exported so a caller implementing its own
// domain.ModSource can report failures these codes already describe, and can test
// its own code against the manager's.
func New(code Code, format string, args ...any) *AppError {
	return appErrors.New(code, format, args...)
}

// Wrap attaches a code and a message to an existing error, preserving it as the
// cause. Wrapping nil returns nil, so a caller can wrap a call's result without
// checking it first.
func Wrap(err error, code Code, format string, args ...any) error {
	return appErrors.Wrap(err, code, format, args...)
}
