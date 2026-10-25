package actions

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCoerceArgsMaterialisesEveryDeclaredOption(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
		{Name: "deep", Type: domain.ActionOptionBool, Label: "Deep", Default: true},
		{Name: "count", Type: domain.ActionOptionNumber, Label: "Count"},
		{Name: "mode", Type: domain.ActionOptionSelect, Label: "Mode", Choices: []string{"a", "b"}},
		{Name: "root", Type: domain.ActionOptionPath, Label: "Root"},
	}

	got, err := coerceArgs(declared, nil)
	require.NoError(t, err, "coerce")

	want := map[string]any{"query": "", "deep": true, "count": 0, "mode": "", "root": ""}
	assert.Equal(t, want, got, "args = %v, want exactly the declared options", got)
}

func TestCoerceArgsDropsUndeclaredOptions(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
	}

	got, err := coerceArgs(declared, map[string]any{"query": "kept", "extra": "dropped"})
	require.NoError(t, err, "coerce")

	assert.NotContains(t, got, "extra", "an undeclared option reached the handler")

	assert.Equal(t, "kept", got["query"], "query = %#v", got["query"])
}

// The CLI's flag name is idiomatic on a shell, the handler's key a Go
// identifier; accepting both means neither surface has to translate.
func TestCoerceArgsAcceptsTheFlagSpelling(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "load_order_file", Type: domain.ActionOptionString, Label: "File"},
	}

	got, err := coerceArgs(declared, map[string]any{"load-order-file": "/mods/mod_load_order.txt"})
	require.NoError(t, err, "coerce")

	assert.Equal(t, "/mods/mod_load_order.txt", got["load_order_file"], "args = %v", got)
}

func TestCoerceArgsRequiresTheRequiredOption(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "query", Type: domain.ActionOptionString, Label: "Query", Required: true},
	}

	_, err := coerceArgs(declared, nil)
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want INVALID_ARGUMENT", err)

	_, err = coerceArgs(declared, map[string]any{"query": ""})
	assert.NoError(t, err, "an explicit empty value should satisfy a required option: %v", err)
}

func TestCoerceBoolAcceptsTheUsualSpellings(t *testing.T) {
	cases := map[string]bool{
		"true": true, "TRUE": true, " yes ": true, "y": true, "on": true, "1": true, "t": true,
		"false": false, "NO": false, "n": false, "off": false, "0": false, "f": false,
	}

	for text, want := range cases {
		got, err := coerceArgs(
			[]domain.ActionOption{{Name: "flag", Type: domain.ActionOptionBool, Label: "Flag"}},
			map[string]any{"flag": text},
		)
		require.NoError(t, err, "%q: %v", text, err)

		assert.Equal(t, want, got["flag"], "%q = %#v, want %#v", text, got["flag"], want)
	}
}

func TestCoerceBoolRefusesUnrecognisedText(t *testing.T) {
	_, err := coerceArgs(
		[]domain.ActionOption{{Name: "flag", Type: domain.ActionOptionBool, Label: "Flag"}},
		map[string]any{"flag": "maybe"},
	)

	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want INVALID_ARGUMENT", err)
}

func TestCoerceNumberSplitsIntegralFromFractional(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "count", Type: domain.ActionOptionNumber, Label: "Count"},
	}

	cases := []struct {
		value any
		want  any
		name  string
	}{
		{name: "string integer", value: "42", want: 42},
		{name: "negative", value: "-7", want: -7},
		{name: "int", value: 5, want: 5},
		{name: "float that is whole", value: 5.0, want: 5},
		{name: "float that is not", value: 5.5, want: 5.5},
		{name: "json number", value: json.Number("12"), want: 12},
		{name: "fractional json number", value: json.Number("0.25"), want: 0.25},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := coerceArgs(declared, map[string]any{"count": tc.value})
			require.NoError(t, err, "coerce")

			assert.Equal(t, tc.want, got["count"], "count = %#v, want %#v", got["count"], tc.want)
		})
	}
}

// A hand-written JSON payload can carry "NaN", which no arithmetic can use.
func TestCoerceNumberRefusesValuesWithoutMeaning(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "count", Type: domain.ActionOptionNumber, Label: "Count"},
	}

	for _, value := range []string{"NaN", "Inf", "-Inf", "lots", ""} {
		_, err := coerceArgs(declared, map[string]any{"count": value})
		assert.Error(t, err, "%q was accepted", value)
	}

	for _, value := range []any{math.NaN(), math.Inf(1)} {
		_, err := coerceArgs(declared, map[string]any{"count": value})
		assert.Error(t, err, "%v was accepted", value)
	}
}

func TestCoerceNumberRefusesValuesOutOfRange(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "count", Type: domain.ActionOptionNumber, Label: "Count"},
	}

	huge := "99999999999999999999999999"
	_, err := coerceArgs(declared, map[string]any{"count": huge})
	assert.Error(t, err, "an out-of-range number was accepted")
}

func TestCoerceSelectAcceptsOnlyDeclaredChoices(t *testing.T) {
	declared := []domain.ActionOption{{
		Name: "mode", Type: domain.ActionOptionSelect, Label: "Mode",
		Choices: []string{"strict", "lenient"},
	}}

	got, err := coerceArgs(declared, map[string]any{"mode": "lenient"})
	require.NoError(t, err, "coerce")

	assert.Equal(t, "lenient", got["mode"], "mode = %#v", got["mode"])

	_, err = coerceArgs(declared, map[string]any{"mode": "whatever"})
	assert.Error(t, err, "an undeclared choice was accepted")
}

// An empty selection means "use the default"; only a required select must have
// an answer.
func TestCoerceSelectAllowsAnEmptyOptionalAnswer(t *testing.T) {
	declared := []domain.ActionOption{{
		Name: "mode", Type: domain.ActionOptionSelect, Label: "Mode",
		Choices: []string{"strict"},
	}}

	got, err := coerceArgs(declared, map[string]any{"mode": ""})
	require.NoError(t, err, "coerce")

	assert.Equal(t, "", got["mode"], "mode = %#v", got["mode"])

	declared[0].Required = true

	_, err = coerceArgs(declared, map[string]any{"mode": ""})
	assert.Error(t, err, "an empty required select was accepted")
}

// A nested object handed to a text option would need an invented rendering.
func TestCoerceTextRefusesContainers(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
	}

	for _, value := range []any{map[string]any{"a": 1}, []any{1, 2}, struct{}{}} {
		_, err := coerceArgs(declared, map[string]any{"query": value})
		assert.Error(t, err, "%T was accepted", value)
	}
}

// A frontend numeric field sends a number for an untyped text input; refusing it
// would let the form's control type decide whether the action runs.
func TestCoerceTextRendersScalars(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
	}

	got, err := coerceArgs(declared, map[string]any{"query": 12})
	require.NoError(t, err, "coerce")

	assert.Equal(t, "12", got["query"], "query = %#v", got["query"])
}

func TestCoerceArgsRejectsUnknownType(t *testing.T) {
	_, err := coerceArgs(
		[]domain.ActionOption{{Name: "query", Type: "colour", Label: "Query"}},
		map[string]any{"query": "x"},
	)

	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error = %v, want INVALID_ARGUMENT", err)
}

func TestCoerceArgsReportsTheOffendingOption(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
		{Name: "count", Type: domain.ActionOptionNumber, Label: "Count"},
	}

	err := func() error {
		_, err := coerceArgs(declared, map[string]any{"query": "fine", "count": "nope"})
		return err
	}()
	require.Error(t, err, "expected an error")

	assert.NotEqual(t, "", appErrors.Message(err), "the error carries no message for the user")
}
