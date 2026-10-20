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

	if err := reg.Register(okAction("test", "test:do")); err != nil {
		t.Fatalf("register: %v", err)
	}

	got, ok := reg.Get("test:do")
	if !ok {
		t.Fatal("action not found after registration")
	}

	if got.ID != "test:do" || got.GameID != "test" {
		t.Fatalf("unexpected action %+v", got)
	}

	if !reg.Has("test:do") || reg.Has("test:missing") {
		t.Fatal("Has disagrees with Get")
	}

	if reg.Len() != 1 {
		t.Fatalf("Len = %d, want 1", reg.Len())
	}
}

// Registration replaces, so a suite that registers a fixture repeatedly need
// not care about leftovers from an earlier test.
func TestRegistryRegisterReplaces(t *testing.T) {
	reg := NewRegistry()

	if err := reg.Register(okAction("test", "test:do")); err != nil {
		t.Fatalf("first register: %v", err)
	}

	updated := okAction("test", "test:do")
	updated.DisplayName = "second"

	if err := reg.Register(updated); err != nil {
		t.Fatalf("second register: %v", err)
	}

	if reg.Len() != 1 {
		t.Fatalf("Len = %d, want 1 after replacement", reg.Len())
	}

	got, _ := reg.Get("test:do")
	if got.DisplayName != "second" {
		t.Fatalf("DisplayName = %q, want second", got.DisplayName)
	}
}

// A game publishes its own actions, so a game that publishes none costs nothing.
func TestRegistryRegisterGame(t *testing.T) {
	reg := NewRegistry()

	if err := reg.RegisterGame(&fakeGame{id: "alpha", actions: 2}); err != nil {
		t.Fatalf("register game: %v", err)
	}

	if reg.Len() != 2 {
		t.Fatalf("Len = %d, want 2", reg.Len())
	}

	if err := reg.RegisterGame(&fakeGame{id: "beta"}); err != nil {
		t.Fatalf("register game without actions: %v", err)
	}

	if reg.Len() != 2 {
		t.Fatalf("Len = %d, want 2", reg.Len())
	}
}

func TestRegistryRegisterNilGame(t *testing.T) {
	reg := NewRegistry()

	err := reg.RegisterGame(nil)
	if err == nil {
		t.Fatal("expected an error for a nil game")
	}

	if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
		t.Fatalf("error %v does not carry INVALID_ARGUMENT", err)
	}
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
			if err == nil {
				t.Fatal("expected registration to be refused")
			}

			if !appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument) {
				t.Fatalf("error %v does not carry INVALID_ARGUMENT", err)
			}

			if reg.Len() != 0 {
				t.Fatal("a refused action was stored anyway")
			}
		})
	}
}

func TestRegistryNilReceiverIsSafe(t *testing.T) {
	var reg *Registry

	if err := reg.Register(okAction("test", "test:do")); err == nil {
		t.Fatal("expected an error from a nil registry")
	}

	if err := reg.RegisterGame(&fakeGame{id: "test"}); err == nil {
		t.Fatal("expected an error from a nil registry")
	}

	if _, ok := reg.Get("test:do"); ok {
		t.Fatal("a nil registry reported an action")
	}

	if reg.Has("test:do") || reg.Len() != 0 {
		t.Fatal("a nil registry reported state")
	}

	if reg.ListAll() != nil || reg.ListByGame("test") != nil ||
		reg.ListAvailable("test", domain.PlatformLinux) != nil {
		t.Fatal("a nil registry listed actions")
	}
}

func TestRegistryListsAreStableAndFiltered(t *testing.T) {
	reg := NewRegistry()

	for _, id := range []string{"b:two", "a:two", "a:one"} {
		if err := reg.Register(okAction(id[:1], id)); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	linuxOnly := okAction("a", "a:linux_only")
	linuxOnly.Platform = domain.ActionPlatformLinux

	if err := reg.Register(linuxOnly); err != nil {
		t.Fatalf("register linux-only: %v", err)
	}

	all := idsOf(reg.ListAll())
	if len(all) != 4 || all[0] != "a:linux_only" || all[3] != "b:two" {
		t.Fatalf("ListAll = %v, want id order", all)
	}

	if got := idsOf(reg.ListByGame("a")); len(got) != 3 || got[0] != "a:linux_only" ||
		got[2] != "a:two" {
		t.Fatalf("ListByGame(a) = %v", got)
	}

	avail := idsOf(reg.ListAvailable("a", domain.PlatformLinux))
	if len(avail) != 3 {
		t.Fatalf("ListAvailable(linux) = %v, want the three universal actions", avail)
	}

	avail = idsOf(reg.ListAvailable("a", domain.PlatformWindows))
	if len(avail) != 2 {
		t.Fatalf("ListAvailable(windows) = %v, want two", avail)
	}

	if got := reg.ListByGame("nobody"); len(got) != 0 {
		t.Fatalf("ListByGame(nobody) = %v, want empty", got)
	}
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
