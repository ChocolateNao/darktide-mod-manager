package actions

import (
	"context"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// okAction is the smallest action that passes validation. Tests mutate it so a
// rule change shows up as one failing test.
func okAction(gameID, id string) domain.GameAction {
	action := domain.GameAction{
		ID:     domain.ActionID(id),
		GameID: gameID,
		Handler: func(context.Context, domain.ActionContext) (*domain.GameActionResult, error) {
			return &domain.GameActionResult{Success: true, Message: "ok"}, nil
		},
	}

	return action
}

func TestRegistryRegisterAndGet(t *testing.T) {
	reg := NewRegistry()

	require.NoError(t, reg.Register(okAction("test", "test:do")), "register")

	got, ok := reg.Get("test:do")
	require.True(t, ok, "action not found after registration")

	assert.Equal(t, domain.ActionID("test:do"), got.ID, "unexpected action %+v", got)
	assert.Equal(t, "test", got.GameID, "unexpected action %+v", got)

	assert.True(t, reg.Has("test:do"), "Has disagrees with Get")
	assert.False(t, reg.Has("test:missing"), "Has disagrees with Get")

	assert.Equal(t, 1, reg.Len(), "Len = %d, want 1", reg.Len())
}

// Registration replaces, so a suite that registers a fixture repeatedly need
// not care about leftovers from an earlier test.
func TestRegistryRegisterReplaces(t *testing.T) {
	reg := NewRegistry()

	require.NoError(t, reg.Register(okAction("test", "test:do")), "first register")

	updated := okAction("test", "test:do")
	updated.DisplayName = "second"

	require.NoError(t, reg.Register(updated), "second register")

	assert.Equal(t, 1, reg.Len(), "Len = %d, want 1 after replacement", reg.Len())

	got, _ := reg.Get("test:do")
	assert.Equal(t, "second", got.DisplayName, "DisplayName = %q, want second", got.DisplayName)
}

// A game publishes its own actions, so a game that publishes none costs nothing.
func TestRegistryRegisterGame(t *testing.T) {
	reg := NewRegistry()

	require.NoError(t, reg.RegisterGame(&fakeGame{id: "alpha", actions: 2}), "register game")

	assert.Equal(t, 2, reg.Len(), "Len = %d, want 2", reg.Len())

	require.NoError(t, reg.RegisterGame(&fakeGame{id: "beta"}), "register game without actions")

	assert.Equal(t, 2, reg.Len(), "Len = %d, want 2", reg.Len())
}

func TestRegistryRegisterNilGame(t *testing.T) {
	reg := NewRegistry()

	err := reg.RegisterGame(nil)
	require.Error(t, err, "expected an error for a nil game")

	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
		"error %v does not carry INVALID_ARGUMENT", err)
}

func TestRegistryRegisterRejectsInvalidActions(t *testing.T) {
	cases := []struct {
		name   string
		action domain.GameAction
	}{
		{
			name:   "id without game prefix",
			action: okAction("test", "do"),
		},
		{
			name:   "id prefix belongs to another game",
			action: okAction("test", "other:do"),
		},
		{
			name: "handler and script together",
			action: domain.GameAction{
				ID:      "test:do",
				GameID:  "test",
				Handler: okAction("test", "test:do").Handler,
				Script:  &domain.ActionScript{Commands: []domain.ActionCommand{{Path: "true"}}},
			},
		},
		{
			name:   "neither handler nor script",
			action: domain.GameAction{ID: "test:do", GameID: "test"},
		},
		{
			name:   "empty game id",
			action: okAction("", "test:do"),
		},
		{
			name:   "unknown platform",
			action: withPlatform(okAction("test", "test:do"), "plan9"),
		},
		{
			name: "option without a label",
			action: withOptions(okAction("test", "test:do"), domain.ActionOption{
				Name: "mode", Type: domain.ActionOptionString,
			}),
		},
		{
			name: "select without choices",
			action: withOptions(okAction("test", "test:do"), domain.ActionOption{
				Name: "mode", Type: domain.ActionOptionSelect, Label: "Mode",
			}),
		},
		{
			name: "duplicate option names",
			action: withOptions(okAction("test", "test:do"),
				domain.ActionOption{Name: "mode", Type: domain.ActionOptionString, Label: "Mode"},
				domain.ActionOption{Name: "mode", Type: domain.ActionOptionBool, Label: "Mode"},
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()

			err := reg.Register(tc.action)
			require.Error(t, err, "expected registration to be refused")

			assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument),
				"error %v does not carry INVALID_ARGUMENT", err)

			assert.Equal(t, 0, reg.Len(), "a refused action was stored anyway")
		})
	}
}

func TestRegistryNilReceiverIsSafe(t *testing.T) {
	var reg *Registry

	require.Error(t, reg.Register(okAction("test", "test:do")),
		"expected an error from a nil registry")

	require.Error(t, reg.RegisterGame(&fakeGame{id: "test"}),
		"expected an error from a nil registry")

	_, ok := reg.Get("test:do")
	assert.False(t, ok, "a nil registry reported an action")

	assert.False(t, reg.Has("test:do"), "a nil registry reported state")
	assert.Equal(t, 0, reg.Len(), "a nil registry reported state")

	assert.Nil(t, reg.ListAll(), "a nil registry listed actions")
	assert.Nil(t, reg.ListByGame("test"), "a nil registry listed actions")
	assert.Nil(t, reg.ListAvailable("test", domain.PlatformLinux), "a nil registry listed actions")
}

func TestRegistryListsAreStableAndFiltered(t *testing.T) {
	reg := NewRegistry()

	for _, id := range []string{"b:two", "a:two", "a:one"} {
		require.NoError(t, reg.Register(okAction(id[:1], id)), "register %s", id)
	}

	linuxOnly := okAction("a", "a:linux_only")
	linuxOnly.Platform = domain.ActionPlatformLinux

	require.NoError(t, reg.Register(linuxOnly), "register linux-only")

	all := idsOf(reg.ListAll())
	assert.Len(t, all, 4, "ListAll = %v, want id order", all)
	assert.Equal(t, domain.ActionID("a:linux_only"), all[0], "ListAll = %v, want id order", all)
	assert.Equal(t, domain.ActionID("b:two"), all[3], "ListAll = %v, want id order", all)

	got := idsOf(reg.ListByGame("a"))
	assert.Len(t, got, 3, "ListByGame(a) = %v", got)
	assert.Equal(t, domain.ActionID("a:linux_only"), got[0], "ListByGame(a) = %v", got)
	assert.Equal(t, domain.ActionID("a:two"), got[2], "ListByGame(a) = %v", got)

	avail := idsOf(reg.ListAvailable("a", domain.PlatformLinux))
	assert.Len(t, avail, 3, "ListAvailable(linux) = %v, want the three universal actions", avail)

	avail = idsOf(reg.ListAvailable("a", domain.PlatformWindows))
	assert.Len(t, avail, 2, "ListAvailable(windows) = %v, want two", avail)

	assert.Empty(t, reg.ListByGame("nobody"), "ListByGame(nobody) = %v, want empty")
}

func idsOf(actions []domain.GameAction) []domain.ActionID {
	out := make([]domain.ActionID, 0, len(actions))
	for _, action := range actions {
		out = append(out, action.ID)
	}

	return out
}

func withPlatform(action domain.GameAction, platform domain.ActionPlatform) domain.GameAction {
	action.Platform = platform

	return action
}

func withOptions(action domain.GameAction, options ...domain.ActionOption) domain.GameAction {
	action.Options = options

	return action
}

// Platform universality and game scoping are separate questions. `open_workshop`
// declares every platform, `windows_only` is scoped to one, and neither answer
// may depend on which game is selected.
func TestListAvailableGatesOnPlatformNotOnInstallation(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()

	for _, action := range []domain.GameAction{
		{ID: "darktide:open_workshop", GameID: "darktide", RequiresGame: false},
		{ID: "darktide:validate_loadorder", GameID: "darktide", RequiresGame: true},
		{ID: "other:thing", GameID: "other"},
	} {
		action.DisplayName = string(action.ID)
		action.Handler = func(context.Context, domain.ActionContext) (*domain.GameActionResult, error) {
			return &domain.GameActionResult{Success: true}, nil
		}
		require.NoError(t, registry.Register(action), "register %s", action.ID)
	}

	registry.Register(domain.GameAction{
		ID: "darktide:anywhere", GameID: "darktide",
		DisplayName: "Anywhere", Platform: domain.ActionPlatformUniversal,
		Handler: func(context.Context, domain.ActionContext) (*domain.GameActionResult, error) {
			return &domain.GameActionResult{Success: true}, nil
		},
	})
	registry.Register(domain.GameAction{
		ID: "darktide:windows_only", GameID: "darktide",
		DisplayName: "Windows only", Platform: domain.ActionPlatformWindows,
		Handler: func(context.Context, domain.ActionContext) (*domain.GameActionResult, error) {
			return &domain.GameActionResult{Success: true}, nil
		},
	})

	ids := make([]string, 0, 4)
	for _, action := range registry.ListAvailable("darktide", domain.PlatformLinux) {
		ids = append(ids, string(action.ID))
	}

	assert.Contains(t, ids, "darktide:open_workshop",
		"needs no installation, so it is offered anyway")
	assert.Contains(t, ids, "darktide:validate_loadorder")
	assert.Contains(t, ids, "darktide:anywhere", "a universal action runs everywhere")
	assert.NotContains(
		t,
		ids,
		"darktide:windows_only",
		"not offered on a platform it cannot run on",
	)
	assert.NotContains(t, ids, "other:thing", "another game's action is not ours to offer")
}
