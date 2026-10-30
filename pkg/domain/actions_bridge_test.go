package domain

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
 * An action's behaviour is a Go func, and `encoding/json` refuses a func type
 * outright instead of skipping it. That made an untagged `Handler` fatal the
 * moment anything listed actions across the Wails bridge — the whole application
 * closed on startup with "json: unsupported type: domain.GameActionHandler".
 *
 * These cases pin the shape for every type an action listing carries, because
 * the failure mode is a process exit: not an error a caller can handle.
 */
func TestGameActionCarriesItsDeclarationWithoutItsBehaviour(t *testing.T) {
	t.Parallel()

	id, err := NewActionID("darktide", "validate_loadorder")
	require.NoError(t, err)

	action := GameAction{
		Handler: func(_ context.Context, _ ActionContext) (*GameActionResult, error) {
			return &GameActionResult{Success: true}, nil
		},
		Script:       &ActionScript{Interpreter: "sh"},
		ID:           id,
		GameID:       "darktide",
		DisplayName:  "Validate load order",
		Category:     "mods",
		Platform:     ActionPlatformUniversal,
		RequiresGame: true,
	}

	encoded, err := json.Marshal(action)
	require.NoError(t, err, "an action declaration must be serialisable")

	var decoded GameAction
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	assert.Equal(t, action.ID, decoded.ID)
	assert.Equal(t, action.DisplayName, decoded.DisplayName)
	assert.Equal(t, action.Category, decoded.Category)
	assert.Nil(t, decoded.Handler, "the behaviour is reached by ID, never by value")
}

func TestGameActionResultWithUnstructuredDataSurvivesTheBridge(t *testing.T) {
	t.Parallel()

	result := GameActionResult{
		Success: true,
		Message: "ok",
		Output:  "3 entries",
		Data: map[string]any{
			"path":      "/mods/mod_load_order.txt",
			"present":   true,
			"entries":   []any{"a", "b", "c"},
			"errors":    []any{},
			"duplicate": true,
		},
	}

	encoded, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded GameActionResult
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	assert.True(t, decoded.Success)
	assert.Equal(t, result.Data, decoded.Data, "data is opaque to the bridge and must arrive whole")
}

/*
 * `ActionOption.Default` is `any`, so a declaration is free to put something
 * JSON cannot represent in it: that is a programming error, not a bridge
 * failure, but the same "the process exits" symptom makes it worth pinning.
 */
func TestActionOptionDeclaresAJSONSafeDefault(t *testing.T) {
	t.Parallel()

	for _, option := range []ActionOption{
		{Name: "workshop_id", Type: ActionOptionString, Label: "Workshop id", Required: true},
		{Name: "verbose", Type: ActionOptionBool, Label: "Verbose", Default: false},
		{Name: "retries", Type: ActionOptionNumber, Label: "Retries", Default: 3},
		{Name: "mode", Type: ActionOptionSelect, Label: "Mode", Choices: []string{"a", "b"}, Default: "a"},
	} {
		_, err := json.Marshal(option)
		require.NoErrorf(t, err, "option %q must be serialisable", option.Name)
	}
}

func TestActionOptionRejectsANonJSONDefault(t *testing.T) {
	t.Parallel()

	option := ActionOption{
		Name:    "callback",
		Type:    ActionOptionString,
		Label:   "Callback",
		Default: func() {},
	}

	_, err := json.Marshal(option)
	require.Error(t, err, "this is the shape the bridge cannot carry, and the guard belongs here")
}
