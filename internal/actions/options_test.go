package actions

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
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
	if err != nil {
		t.Fatalf("coerce: %v", err)
	}

	want := map[string]any{"query": "", "deep": true, "count": 0, "mode": "", "root": ""}
	for key, expected := range want {
		if got[key] != expected {
			t.Fatalf("%s = %#v, want %#v", key, got[key], expected)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("args = %v, want exactly the declared options", got)
	}
}

func TestCoerceArgsDropsUndeclaredOptions(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
	}

	got, err := coerceArgs(declared, map[string]any{"query": "kept", "extra": "dropped"})
	if err != nil {
		t.Fatalf("coerce: %v", err)
	}

	if _, ok := got["extra"]; ok {
		t.Fatal("an undeclared option reached the handler")
	}

	if got["query"] != "kept" {
		t.Fatalf("query = %#v", got["query"])
	}
}

// The CLI's flag name has to be idiomatic on a shell, the handler's key has to
// be a Go identifier. Accepting both means neither surface has to translate.
func TestCoerceArgsAcceptsTheFlagSpelling(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "load_order_file", Type: domain.ActionOptionString, Label: "File"},
	}

	got, err := coerceArgs(declared, map[string]any{"load-order-file": "/mods/mod_load_order.txt"})
	if err != nil {
		t.Fatalf("coerce: %v", err)
	}

	if got["load_order_file"] != "/mods/mod_load_order.txt" {
		t.Fatalf("args = %v", got)
	}
}

func TestCoerceArgsRequiresTheRequiredOption(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "query", Type: domain.ActionOptionString, Label: "Query", Required: true},
	}

	if _, err := coerceArgs(declared, nil); !appErrors.IsAppError(
		err,
		appErrors.ErrCodeInvalidArgument,
	) {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}

	if _, err := coerceArgs(declared, map[string]any{"query": ""}); err != nil {
		t.Fatalf("an explicit empty value should satisfy a required option: %v", err)
	}
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
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}

		if got["flag"] != want {
			t.Fatalf("%q = %#v, want %#v", text, got["flag"], want)
		}
	}
}

func TestCoerceBoolRefusesUnrecognisedText(t *testing.T) {
	_, err := coerceArgs(
		[]domain.ActionOption{{Name: "flag", Type: domain.ActionOptionBool, Label: "Flag"}},
		map[string]any{"flag": "maybe"},
	)

	if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
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
			if err != nil {
				t.Fatalf("coerce: %v", err)
			}

			if got["count"] != tc.want {
				t.Fatalf("count = %#v, want %#v", got["count"], tc.want)
			}
		})
	}
}

// A JSON payload can carry "NaN" only if something wrote it by hand; accepting
// it would put a value into a handler that no arithmetic can use.
func TestCoerceNumberRefusesValuesWithoutMeaning(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "count", Type: domain.ActionOptionNumber, Label: "Count"},
	}

	for _, value := range []string{"NaN", "Inf", "-Inf", "lots", ""} {
		if _, err := coerceArgs(declared, map[string]any{"count": value}); err == nil {
			t.Fatalf("%q was accepted", value)
		}
	}

	for _, value := range []any{math.NaN(), math.Inf(1)} {
		if _, err := coerceArgs(declared, map[string]any{"count": value}); err == nil {
			t.Fatalf("%v was accepted", value)
		}
	}
}

func TestCoerceNumberRefusesValuesOutOfRange(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "count", Type: domain.ActionOptionNumber, Label: "Count"},
	}

	huge := "99999999999999999999999999"
	if _, err := coerceArgs(declared, map[string]any{"count": huge}); err == nil {
		t.Fatal("an out-of-range number was accepted")
	}
}

func TestCoerceSelectAcceptsOnlyDeclaredChoices(t *testing.T) {
	declared := []domain.ActionOption{{
		Name: "mode", Type: domain.ActionOptionSelect, Label: "Mode",
		Choices: []string{"strict", "lenient"},
	}}

	got, err := coerceArgs(declared, map[string]any{"mode": "lenient"})
	if err != nil {
		t.Fatalf("coerce: %v", err)
	}

	if got["mode"] != "lenient" {
		t.Fatalf("mode = %#v", got["mode"])
	}

	if _, err := coerceArgs(declared, map[string]any{"mode": "whatever"}); err == nil {
		t.Fatal("an undeclared choice was accepted")
	}
}

// An empty selection means "use the default"; the handler decides what that is.
// Only a required select has to have an answer.
func TestCoerceSelectAllowsAnEmptyOptionalAnswer(t *testing.T) {
	declared := []domain.ActionOption{{
		Name: "mode", Type: domain.ActionOptionSelect, Label: "Mode",
		Choices: []string{"strict"},
	}}

	got, err := coerceArgs(declared, map[string]any{"mode": ""})
	if err != nil {
		t.Fatalf("coerce: %v", err)
	}

	if got["mode"] != "" {
		t.Fatalf("mode = %#v", got["mode"])
	}

	declared[0].Required = true

	if _, err := coerceArgs(declared, map[string]any{"mode": ""}); err == nil {
		t.Fatal("an empty required select was accepted")
	}
}

// A handler declared a text option; handing it a nested object would mean
// inventing a rendering. Refusing keeps the declared type honest.
func TestCoerceTextRefusesContainers(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
	}

	for _, value := range []any{map[string]any{"a": 1}, []any{1, 2}, struct{}{}} {
		if _, err := coerceArgs(declared, map[string]any{"query": value}); err == nil {
			t.Fatalf("%T was accepted", value)
		}
	}
}

// A frontend numeric field sends a number for an untyped text input; refusing it
// would make the form's control type decide whether the action runs.
func TestCoerceTextRendersScalars(t *testing.T) {
	declared := []domain.ActionOption{
		{Name: "query", Type: domain.ActionOptionString, Label: "Query"},
	}

	got, err := coerceArgs(declared, map[string]any{"query": 12})
	if err != nil {
		t.Fatalf("coerce: %v", err)
	}

	if got["query"] != "12" {
		t.Fatalf("query = %#v", got["query"])
	}
}

func TestCoerceArgsRejectsUnknownType(t *testing.T) {
	_, err := coerceArgs(
		[]domain.ActionOption{{Name: "query", Type: "colour", Label: "Query"}},
		map[string]any{"query": "x"},
	)

	if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
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
	if err == nil {
		t.Fatal("expected an error")
	}

	if got := appErrors.Message(err); got == "" {
		t.Fatal("the error carries no message for the user")
	}
}
