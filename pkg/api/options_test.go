package api

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathOptionsRejectBlankValues(t *testing.T) {
	// A blank path is not "unset", it is a path of "" that every later join turns
	// into the working directory. Refusing it at construction means the caller
	// learns their option was ignored before anything has been written.
	options := map[string]Option{
		"WithConfigPath":  WithConfigPath("   "),
		"WithGamePath":    WithGamePath(""),
		"WithCachePath":   WithCachePath("\t"),
		"WithStoragePath": WithStoragePath(" "),
	}
	for name, opt := range options {
		assert.Error(t, opt(&settings{}), "%s accepted a blank path", name)
	}
}

func TestPathOptionsStoreTrimmedValues(t *testing.T) {
	set, err := applyOptions([]Option{
		WithConfigPath("  /etc/dmm.json  "),
		WithCachePath("\t/var/cache/dmm\n"),
		WithStoragePath(" /var/lib/dmm "),
	})
	require.NoError(t, err, "applyOptions")

	assert.Equal(t, "/etc/dmm.json", set.configPath, "config path trimmed value")

	assert.Equal(t, "/var/cache/dmm", set.cachePath, "cache path trimmed value")

	assert.Equal(t, "/var/lib/dmm", set.storagePath, "storage path trimmed value")
	// WithGamePath is left out here because it is persisted, not merely stored:
	// the wiring validates it and writes it to the configuration file.
}

func TestApplyOptionsSkipsNilOptions(t *testing.T) {
	// A caller assembling options from a slice with a conditional hole should not
	// have to filter nils out first.
	set, err := applyOptions([]Option{nil, WithConfigPath("/a.json"), nil})
	require.NoError(t, err, "applyOptions with a nil option")

	assert.Equal(t, "/a.json", set.configPath, "config path")
}

func TestApplyOptionsStopsAtTheFirstFailure(t *testing.T) {
	// Options run in order and the first failure ends construction, so a later
	// option cannot half-configure a client that is never built.
	reached := false

	_, err := applyOptions([]Option{
		WithConfigPath(""),
		func(*settings) error { reached = true; return nil },
	})
	require.Error(t, err, "applyOptions accepted a failing option")

	assert.False(t, reached, "applyOptions kept applying options after one failed")
}

func TestWithSourceConfigRejectsABlankID(t *testing.T) {
	assert.Error(
		t,
		WithSourceConfig("   ", map[string]any{})(&settings{}),
		"WithSourceConfig accepted a blank source id",
	)
}

func TestWithSourceConfigDefaultsToEnabled(t *testing.T) {
	// A block that never mentions `enabled` must not disable a source: a caller
	// supplying only a token has said nothing about whether to use the source.
	set, err := applyOptions([]Option{WithSourceConfig("vortex", map[string]any{"api_key": "k"})})
	require.NoError(t, err, "applyOptions")

	block, ok := set.sourceConfigs["vortex"]
	require.True(t, ok, "no configuration was recorded for vortex")

	assert.True(t, block.Enabled, "a block naming only an api key left the source disabled")

	assert.Equal(t, "k", block.APIKey, "api key")
}

func TestWithSourceConfigNormalisesTheSourceID(t *testing.T) {
	// "GitHub" in a hand-built map has to configure the same source as "github",
	// or the user sees a source as configured while the manager reads nothing.
	set, err := applyOptions(
		[]Option{WithSourceConfig("  GitHub  ", map[string]any{"api_key": "k"})},
	)
	require.NoError(t, err, "applyOptions")

	_, ok := set.sourceConfigs["github"]
	assert.True(t, ok,
		"configuration is keyed %v, want it keyed by the normalised id",
		keysOf(set.sourceConfigs),
	)
}

func TestWithSourceConfigMergesRepeatedCalls(t *testing.T) {
	// A caller setting a token and then adjusting one option should not have to
	// repeat the first call's work — and, more importantly, must not lose it.
	set, err := applyOptions([]Option{
		WithSourceConfig("vortex", map[string]any{"api_key": "k", "api_base": "https://a.invalid"}),
		WithSourceConfig("vortex", map[string]any{"options": map[string]any{"topic": "darktide"}}),
	})
	require.NoError(t, err, "applyOptions")

	block := set.sourceConfigs["vortex"]
	assert.Equal(t, "k", block.APIKey, "api key, want the first call's value to survive")

	assert.Equal(t, "https://a.invalid", block.Options["api_base"],
		"api_base, want the first call's value to survive")

	assert.Equal(t, "darktide", block.Options["topic"], "topic, want the second call's value")
}

func TestWithSourceConfigKeepsAnExplicitDisable(t *testing.T) {
	// Once a caller has turned a source off, a later call that adjusts its token
	// must not turn it back on. That is the whole reason enabledStated is tracked
	// separately from the block.
	set, err := applyOptions([]Option{
		WithSourceConfig("nexus", map[string]any{"enabled": false}),
		WithSourceConfig("nexus", map[string]any{"api_key": "k"}),
	})
	require.NoError(t, err, "applyOptions")

	assert.False(t, set.sourceConfigs["nexus"].Enabled,
		"a later block that never mentioned enabled re-enabled a source the caller had turned off")

	assert.Equal(
		t,
		"k",
		set.sourceConfigs["nexus"].APIKey,
		"the later block's api key was not applied",
	)
	// The stated flag has to survive too, and not only the merged block. The
	// wiring asks "did the caller say anything about enabled?" and uses the
	// answer to decide whether the file's value still applies; a second call that
	// stays silent must not answer "no" and hand the file back its say.
	assert.True(
		t,
		set.sourceEnabledStated["nexus"],
		"the second call forgot that the caller had stated enabled",
	)
}

func TestWithSourceConfigEnablesASourceNobodyConfigured(t *testing.T) {
	// The option path seeds a never-seen source as enabled, which is what makes
	// `NewClient(ctx, WithSourceConfig("vortex", ...))` a source the caller can
	// actually search rather than one that is silently off.
	set, err := applyOptions([]Option{WithSourceConfig("vortex", map[string]any{"api_key": "k"})})
	require.NoError(t, err, "applyOptions")

	assert.True(
		t,
		set.sourceConfigs["vortex"].Enabled,
		"naming a source in an option left it disabled",
	)
}

func TestWithSourceConfigDisablesWhenTold(t *testing.T) {
	set, err := applyOptions([]Option{
		WithSourceConfig("nexus", map[string]any{"enabled": true}),
		WithSourceConfig("nexus", map[string]any{"enabled": false}),
	})
	require.NoError(t, err, "applyOptions")

	assert.False(
		t,
		set.sourceConfigs["nexus"].Enabled,
		"an explicit enabled:false did not turn the source off",
	)
}

func TestSourceConfigFromReadsTheStructuralKeys(t *testing.T) {
	block, stated, err := sourceConfigFrom("vortex", map[string]any{
		"enabled": false,
		"apiKey":  "  secret  ",
		"paths":   []string{"/a", " ", "/b"},
	})
	require.NoError(t, err, "sourceConfigFrom")

	assert.False(t, block.Enabled, "enabled=false was not honoured")

	assert.Equal(t, "secret", block.APIKey, "api key trimmed value")

	assert.True(t, stated, "a block that named enabled reported it as unstated")

	want := []string{"/a", "/b"}
	assert.True(t, reflect.DeepEqual(block.Paths, want),
		"paths are %v, want %v — blanks must be dropped, not turned into empty paths",
		block.Paths,
		want,
	)
}

// The credential key has several spellings in the wild because the sources each
// documented their own. Accepting all of them is cheaper than telling a caller
// which one this package happens to prefer.
func TestSourceConfigFromAcceptsEveryCredentialSpelling(t *testing.T) {
	for _, key := range []string{"api_key", "apiKey", "key", "token", "API-KEY"} {
		block, _, err := sourceConfigFrom("s", map[string]any{key: "secret"})
		require.NoError(t, err, "sourceConfigFrom with %q", key)

		assert.Equal(t, "secret", block.APIKey, "sourceConfigFrom with %q", key)
	}
}

func TestSourceConfigFromTurnsUnknownKeysIntoOptions(t *testing.T) {
	// The api package cannot enumerate every plugin's private keys, so anything
	// it does not recognise is handed to the source as a provider option. That is
	// what makes a new plugin option a non-breaking change.
	block, stated, err := sourceConfigFrom("github", map[string]any{
		"api_base":     "https://api.invalid",
		"repositories": []any{"owner/one", "owner/two"},
		"max_releases": 25,
	})
	require.NoError(t, err, "sourceConfigFrom")

	assert.Equal(t, "https://api.invalid", block.Options["api_base"], "api_base option")
	// A list is joined with commas because that is what the providers split on, so
	// a value passed here behaves like the same value read from a config file.
	assert.Equal(
		t,
		"owner/one,owner/two",
		block.Options["repositories"],
		"repositories option, want the list joined with commas",
	)

	assert.Equal(t, "25", block.Options["max_releases"],
		"max_releases option; options are %v — a provider option name must survive verbatim",
		keysOf(block.Options),
	)

	assert.False(t, stated, "a block that never named enabled reported it as stated")
}

func TestSourceConfigFromRejectsUnusableValues(t *testing.T) {
	cases := map[string]map[string]any{
		"enabled as a number":          {"enabled": 3},
		"enabled as a word":            {"enabled": "maybe"},
		"a credential as a map":        {"api_key": map[string]any{"a": 1}},
		"a path as a map":              {"paths": map[string]any{"a": 1}},
		"a path list holding a map":    {"paths": []any{map[string]any{}}},
		"an option as a map":           {"api_base": map[string]any{}},
		"an option list holding a map": {"repositories": []any{map[string]any{}}},
	}
	for name, cfg := range cases {
		_, _, err := sourceConfigFrom("s", cfg)
		if err == nil {
			assert.Fail(t, "sourceConfigFrom accepted %s", name)
		} else {
			assert.True(
				t,
				IsAppError(err, ErrCodeInvalidArgument),
				"sourceConfigFor %s returned %v, want an invalid-argument error",
				name,
				err,
			)
		}
	}
}

func TestSourceConfigFromNamesTheOffendingValue(t *testing.T) {
	// An error naming only "invalid argument" leaves a caller guessing which of
	// three keys was wrong.
	_, _, err := sourceConfigFrom("nexus", map[string]any{"enabled": "maybe"})
	require.Error(t, err, "sourceConfigFrom accepted enabled=maybe")

	assert.True(
		t,
		strings.Contains(err.Error(), "nexus") && strings.Contains(err.Error(), "enabled"),
		"error %q does not name the source and the key",
		err,
	)
}

func TestSourceConfigFromAcceptsTheUsualBooleanSpellings(t *testing.T) {
	for _, value := range []any{true, "true", "TRUE", "yes", "on", "1"} {
		block, _, err := sourceConfigFrom("s", map[string]any{"enabled": value})
		require.NoError(t, err, "sourceConfigFrom with enabled=%v", value)

		assert.True(t, block.Enabled, "enabled=%v was read as false", value)
	}

	for _, value := range []any{false, "false", "No", "off", "0"} {
		block, _, err := sourceConfigFrom("s", map[string]any{"enabled": value})
		require.NoError(t, err, "sourceConfigFrom with enabled=%v", value)

		assert.False(t, block.Enabled, "enabled=%v was read as true", value)
	}
}

func TestSourceConfigFromAcceptsANilOrEmptyBlock(t *testing.T) {
	// A caller building a block from optional JSON has a nil map when the
	// document had no "sources" section at all. That is not an error.
	for name, cfg := range map[string]map[string]any{"nil": nil, "empty": {}} {
		block, _, err := sourceConfigFrom("s", cfg)
		require.NoError(t, err, "sourceConfigFrom with a %s block", name)

		assert.True(t, block.Enabled, "a %s block left the source disabled", name)
	}
}

func TestSourceConfigFromReadsPathsFromAString(t *testing.T) {
	// One path passed as a string and a list of paths must produce the same
	// block, or a caller has to know which shape this package prefers.
	fromString, _, err := sourceConfigFrom(
		"local",
		map[string]any{"paths": "/mods, /more\n/again,"},
	)
	require.NoError(t, err, "sourceConfigFrom")

	fromList, _, err := sourceConfigFrom(
		"local",
		map[string]any{"paths": []string{"/mods", "/more", "/again"}},
	)
	require.NoError(t, err, "sourceConfigFrom")

	assert.True(t, reflect.DeepEqual(fromString.Paths, fromList.Paths),
		"a string produced %v and a list produced %v; they must agree",
		fromString.Paths,
		fromList.Paths,
	)

	want := []string{"/mods", "/more", "/again"}
	assert.True(
		t,
		reflect.DeepEqual(fromList.Paths, want),
		"paths are %v, want %v",
		fromList.Paths,
		want,
	)
}

func TestWithEventHandlerCollectsHandlersInOrder(t *testing.T) {
	// Order matters: a caller subscribing two handlers expects them called in the
	// order it wrote them, not in map order.
	var order []string

	set, err := applyOptions([]Option{
		WithEventHandler(func(Event) { order = append(order, "first") }),
		nil,
		WithEventHandler(nil),
		WithEventHandler(func(Event) { order = append(order, "second") }),
	})
	require.NoError(t, err, "applyOptions")

	assert.Len(t, set.handlers, 2,
		"collected handlers, want 2 — a nil handler must be dropped, not stored")

	bus := NewEventBus()
	for _, handler := range set.handlers {
		bus.Subscribe(handler)
	}

	bus.Publish(services.Event{Type: services.EventLog, Data: "line"})

	assert.True(
		t,
		reflect.DeepEqual(order, []string{"first", "second"}),
		"handlers ran in order %v, want [first second]",
		order,
	)
}

func TestWithLoggerKeepsTheLastAndIgnoresNil(t *testing.T) {
	first := &countingLogger{}
	second := &countingLogger{}

	set, err := applyOptions([]Option{WithLogger(first), WithLogger(nil), WithLogger(second)})
	require.NoError(t, err, "applyOptions")

	assert.Equal(t, Logger(second), set.logger, "the last non-nil logger did not win")
}

// countingLogger records what it was asked to print.
type countingLogger struct {
	lines []string
	mu    sync.Mutex
}

func (l *countingLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *countingLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]string, len(l.lines))
	copy(out, l.lines)

	return out
}

// keysOf lists a map's keys, sorted so a failure message is stable and a
// comparison over the result means something.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

// TestLoggerFuncCarriesTheFormat is the adapter's whole contract: the format
// string and its arguments reach the function unchanged, so a caller can wrap
// whatever it likes without this package having to know about it.
func TestLoggerFuncCarriesTheFormat(t *testing.T) {
	var (
		got   string
		count int
	)

	logger := LoggerFunc(func(format string, args ...any) {
		got = fmt.Sprintf(format, args...)
		count++
	})
	logger.Printf("source %q could not be configured: %v", "nexus", errBoom)

	assert.Equal(
		t,
		`source "nexus" could not be configured: boom`,
		got,
		"logged format and arguments unchanged",
	)

	assert.Equal(t, 1, count, "the wrapped function ran times, want 1")
}

// TestNilLoggerFuncDiscards documents that a nil adapter is safe: WithLogger
// drops a nil logger, and a nil LoggerFunc must not be the thing that panics
// later during start-up.
func TestNilLoggerFuncDiscards(t *testing.T) {
	var logger LoggerFunc
	logger.Printf("this must not panic: %v", errBoom)

	assert.Nil(t, logger, "a nil LoggerFunc compared unequal to nil")
}

// errBoom is the stand-in failure used by the tests in this file. It is defined
// here rather than borrowed from another file so the file that uses it does
// not break when that other file is reorganised.
var errBoom = errors.New("boom")
