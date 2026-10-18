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
// Every declared option ends up present — filled from its default, or from its
// type's zero value — so a handler never has to check whether one arrived.
// Undeclared options are dropped: nothing promised their type.
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
// Materialising it rather than omitting the key is what lets a handler treat its
// options uniformly, and gives the frontend a form whose fields all render.
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
// line uses as an alias for the declared snake_case name, so neither surface has
// to translate.
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
// A numeric field from a frontend form is not a mistake for an untyped option, so
// scalars are rendered through their literal text; containers are refused because
// there is no honest text for them.
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
// hand-written JSON payload. The list is explicit because silently accepting a
// value nobody typed is worse than refusing one.
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
// otherwise, so ActionContext.ArgInt works for an amount, a port or a count
// without every caller asserting a type.
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

	// A whole number too large for an int is a typo — a count, a port or a timeout
	// that lost a digit — and a float would hand the handler an unindexable value.
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

	// An empty value is left empty rather than refused: an optional select with
	// nothing chosen means "use whatever is default".
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
