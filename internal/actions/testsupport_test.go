package actions

import (
	"context"
	"fmt"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// fakeGame is a minimal game definition. It implements the whole interface
// explicitly so that a new method there breaks these tests loudly rather than
// silently making the fake unusable.
type fakeGame struct {
	actionErr   error
	id          string
	displayName string
	actions     int
}

func (g *fakeGame) ID() string { return g.id }

func (g *fakeGame) DisplayName() string {
	if g.displayName != "" {
		return g.displayName
	}

	return g.id
}

func (g *fakeGame) Description() string            { return "" }
func (g *fakeGame) SteamAppID() string             { return "" }
func (g *fakeGame) InstallDirNames() []string      { return nil }
func (g *fakeGame) BinaryNames() []string          { return nil }
func (g *fakeGame) VersionMarkers() []string       { return nil }
func (g *fakeGame) ModsPathCandidates() [][]string { return nil }
func (g *fakeGame) ModsDirName() string            { return "mods" }

func (g *fakeGame) ModInstallMethod() string                     { return domain.InstallMethodDirect }
func (g *fakeGame) ManifestNames() []string                      { return nil }
func (g *fakeGame) LoadOrderFile() string                        { return "" }
func (g *fakeGame) InstallModes() []domain.ModInstallMode        { return nil }
func (g *fakeGame) SourceDefaults() map[string]map[string]string { return nil }

// GameActions returns the requested number of trivial actions, or none when
// actionErr is set so a test can prove a validation error surfaces.
func (g *fakeGame) GameActions() []domain.GameAction {
	if g.actionErr != nil {
		return nil
	}

	out := make([]domain.GameAction, 0, g.actions)
	for i := range g.actions {
		out = append(out, okAction(g.id, g.id+":action_"+string(rune('a'+i))))
	}

	return out
}

// stubGame adapts a fakeGame to the game registry's Get signature so the runner
// can be tested without registering it globally.
type stubGame struct {
	def domain.GameDefinition
}

func (s stubGame) Get(string) (domain.GameDefinition, bool) {
	if s.def == nil {
		return nil, false
	}

	return s.def, true
}

// stubResolver is the GameResolver a run consults.
type stubResolver struct {
	def         domain.GameDefinition
	activeErr   error
	currentErr  error
	modsErr     error
	install     *domain.GameInstallation
	modsPath    string
	settings    domain.GameSettings
	currentCall int
}

func (s *stubResolver) Active() (domain.GameDefinition, domain.GameSettings, error) {
	if s.activeErr != nil {
		return nil, domain.GameSettings{}, s.activeErr
	}

	def := s.def
	if def == nil {
		def = &fakeGame{id: "test"}
	}

	return def, s.settings, nil
}

func (s *stubResolver) Current(context.Context) (*domain.GameInstallation, error) {
	s.currentCall++

	if s.currentErr != nil {
		return nil, s.currentErr
	}

	if s.install != nil {
		return s.install, nil
	}

	return &domain.GameInstallation{Path: "/games/test", Valid: true}, nil
}

func (s *stubResolver) GetModsPath(*domain.GameInstallation) (string, error) {
	if s.modsErr != nil {
		return "", s.modsErr
	}

	return s.modsPath, nil
}

// recordingOpener stands in for the desktop opener so a test never shells out.
type recordingOpener struct {
	dirErr error
	urlErr error
	dirs   []string
	urls   []string
}

func (o *recordingOpener) OpenDirectory(_ context.Context, dir string) error {
	o.dirs = append(o.dirs, dir)

	return o.dirErr
}

func (o *recordingOpener) OpenURL(_ context.Context, rawURL string) error {
	o.urls = append(o.urls, rawURL)

	return o.urlErr
}

// recordingCommandRunner captures what a script action tried to run.
type recordingCommandRunner struct {
	err    error
	output CommandOutput
	specs  []CommandSpec
}

func (r *recordingCommandRunner) Run(_ context.Context, spec CommandSpec) (CommandOutput, error) {
	r.specs = append(r.specs, spec)

	if r.err != nil {
		return CommandOutput{}, r.err
	}

	return r.output, nil
}

// echoLogger collects the lines streamed to it.
type echoLogger struct {
	sb strings.Builder
}

func (l *echoLogger) Printf(format string, args ...any) {
	l.sb.WriteString(strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
	l.sb.WriteString("\n")
}

func (l *echoLogger) text() string { return l.sb.String() }
