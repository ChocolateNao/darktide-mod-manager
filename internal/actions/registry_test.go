package actions

import (
	"context"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// okAction is the smallest action that passes validation: a handler, no
// options, no platform restriction. Tests mutate it rather than building their
// own so that a change to the validation rules shows up as one failing test
// instead of fifty.
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

// Registration replaces rather than refusing: game definitions are registered at
// start-up, and a test suite that registers a fixture repeatedly should not
// have to care about leftovers from an earlier test.
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

// A game publishes its own actions, so supporting a new game never means
// editing the registry. A game that publishes none must cost nothing.
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
