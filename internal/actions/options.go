// Package actions holds the registry and runner behind game-specific actions.
//
// An action is a named operation a game definition publishes: "validate the load
// order", "open the mods folder", "launch the game". They are declared in Go
// rather than in configuration because a handler is a function, and the three
// surfaces that run them — the API, the Wails bindings and the command line —
// have to agree on one definition of what each one takes.
//
// The package is split so that each piece can be reasoned about alone:
//
//   - registry.go answers "which actions exist" and refuses a malformed one at
//     registration rather than at the moment a user presses the button.
//   - options.go answers "what values were supplied", coercing whatever the
//     surface produced (a JSON value, a CLI string) into the declared types.
//   - runner.go answers "run it", assembling the context a handler sees and
//     dispatching either an in-process handler or an external command line.
//   - command.go owns the external process: it is the only place in this package
//     that executes anything, which is what keeps that surface small enough to
//     reason about and to fake in tests.
package actions

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// coerceArgs turns caller-supplied values into the types an action declared.
//
// Every declared option ends up present in the result — filled from its default,
// or from the zero value of its type when it has neither a default nor a
// supplied value — so a handler reads an option without first checking whether it
// arrived. Options the action never declared are dropped rather than passed
// through: a handler that reads a key nobody declared would be reading a value
// whose type nothing promised.
func coerceArgs(declared []domain.ActionOption, supplied map[string]any) (map[string]any, error) {
	coerced := make(map[string]any, len(declared))

	for _, option := range declared {
		raw, present := lookup(supplied, option.Name)

		switch {
		case !present || raw == nil:
			if option.Required && option.Default == nil {
				return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
					"action option %q is required", option.Name)
			}

			if option.Default != nil {
				raw = option.Default

				break
			}

			coerced[option.Name] = zeroValue(option.Type)

			continue
		}

		value, err := coerce(option, raw)
		if err != nil {
			return nil, err
		}

		coerced[option.Name] = value
	}

	return coerced, nil
}

// zeroValue is what an option nobody answered reads as.
//
// Materialising it — rather than omitting the key — is what lets a handler treat
// its options uniformly, and it gives the frontend a form whose fields all have
// a value to render.
func zeroValue(optionType domain.ActionOptionType) any {
	switch optionType {
	case domain.ActionOptionBool:
		return false
	case domain.ActionOptionNumber:
		return 0
	case domain.ActionOptionString, domain.ActionOptionPath, domain.ActionOptionSelect:
		return ""
	default:
		// Registration rejects unknown types, so this is unreachable in
		// practice; "" is the least surprising value for a caller that gets here.
		return ""
	}
}

// lookup finds an option's value, accepting the kebab-case spelling the command
// line uses as an alias for the declared snake_case name.
//
// The alias exists because the CLI's flag name has to be idiomatic on a shell
// while the handler's map key has to be a Go identifier; letting both spellings
// through means a caller never has to know which one the runner wants.
func lookup(supplied map[string]any, name string) (any, bool) {
	if value, ok := supplied[name]; ok {
		return value, true
	}

	alias := domain.FormatActionArg(name)
	if alias != name {
		if value, ok := supplied[alias]; ok {
			return value, true
		}
	}

	return nil, false
}

// coerce converts one raw value to the option's declared type.
func coerce(option domain.ActionOption, raw any) (any, error) {
	switch option.Type {
	case domain.ActionOptionString, domain.ActionOptionPath:
		return coerceText(option, raw)
	case domain.ActionOptionBool:
		return coerceBool(option, raw)
	case domain.ActionOptionNumber:
		return coerceNumber(option, raw)
	case domain.ActionOptionSelect:
		return coerceSelect(option, raw)
	default:
		// Registration validates the type, so this is unreachable through the
		// runner; it is a programming error rather than a caller error.
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action option %q has unknown type %q", option.Name, option.Type)
	}
}

// coerceText accepts anything that is textual once rendered.
//
// A path option arriving as a JSON number is nonsense, but a *string* option
// arriving as one is not: a frontend form that sends a numeric field for an
// untyped option should not have to care which option it is rendering. Rendering
// scalars through their literal text is therefore allowed, while containers are
// refused because there is no honest text for them.
func coerceText(option domain.ActionOption, raw any) (any, error) {
	switch value := raw.(type) {
	case string:
		return value, nil
	case bool:
		return strconv.FormatBool(value), nil
	case json.Number:
		return value.String(), nil
	case int:
		return strconv.Itoa(value), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	case nil:
		return "", nil
	default:
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action option %q expects text, got %T", option.Name, raw)
	}
}

// truthyValues are the spellings a boolean accepts from a command line or a
// hand-written JSON payload. The list is deliberately explicit: strconv's
// ParseBool would accept "1"/"t" but not "yes", and silently accepting a value
// nobody typed is worse than refusing one.
var truthyValues = map[string]struct{}{
	"1": {}, "t": {}, "true": {}, "yes": {}, "y": {}, "on": {},
}

var falsyValues = map[string]struct{}{
	"0": {}, "f": {}, "false": {}, "no": {}, "n": {}, "off": {},
}

func coerceBool(option domain.ActionOption, raw any) (any, error) {
	switch value := raw.(type) {
	case bool:
		return value, nil
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(value))
		if _, ok := truthyValues[trimmed]; ok {
			return true, nil
		}

		if _, ok := falsyValues[trimmed]; ok {
			return false, nil
		}

		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action option %q expects a boolean, got %q", option.Name, value)
	case int:
		return value != 0, nil
	case float64:
		return value != 0, nil
	case json.Number:
		return value.String() != "0", nil
	default:
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action option %q expects a boolean, got %T", option.Name, raw)
	}
}

// coerceNumber produces an int when the value is integral and a float64
// otherwise.
//
// The split is what lets ActionContext.ArgInt work for the overwhelmingly common
// case — an amount, a port, a count — without every caller having to assert a
// type, while a genuine fractional option still arrives as a float.
func coerceNumber(option domain.ActionOption, raw any) (any, error) {
	switch value := raw.(type) {
	case int:
		return value, nil
	case int64:
		return fitInt(value)
	case float64:
		return fitFloat(value)
	case json.Number:
		if i, err := value.Int64(); err == nil {
			return fitInt(i)
		}

		f, err := value.Float64()
		if err != nil {
			return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
				"action option %q expects a number, got %q", option.Name, value.String())
		}

		return fitFloat(f)
	case string:
		trimmed := strings.TrimSpace(value)
		if i, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return fitInt(i)
		}

		f, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
				"action option %q expects a number, got %q", option.Name, value)
		}

		return fitFloat(f)
	default:
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"action option %q expects a number, got %T", option.Name, raw)
	}
}

func fitInt(value int64) (any, error) {
	if value < math.MinInt || value > math.MaxInt {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"number %d does not fit in an int", value)
	}

	return int(value), nil
}

func fitFloat(value float64) (any, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"%v is not a usable number", value)
	}

	if value != math.Trunc(value) {
		return value, nil
	}

	// A whole number too large for an int is a typo — a count, a port or a
	// timeout that lost a digit — and returning it as a float would hand the
	// handler a value nothing can index with.
	if value < math.MinInt || value > math.MaxInt {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"%v does not fit in an int", value)
	}

	return int(value), nil
}

func coerceSelect(option domain.ActionOption, raw any) (any, error) {
	value, err := coerceText(option, raw)
	if err != nil {
		return nil, err
	}

	text, _ := value.(string)

	// An empty value is left empty rather than refused: a select whose options
	// are all optional is a legitimate way to say "use whatever is default",
	// and the required check below is what makes it mandatory when it is not.
	for _, choice := range option.Choices {
		if choice == text {
			return text, nil
		}
	}

	if strings.TrimSpace(text) == "" && !option.Required {
		return "", nil
	}

	return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
		"action option %q expects one of [%s], got %q",
		option.Name, strings.Join(option.Choices, ", "), text)
}
