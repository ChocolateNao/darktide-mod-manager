package api

import (
	"context"
	"sync"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// stubSource is a ModSource that refuses the operations a test has not set up.
//
// Refusing rather than answering with a zero value is deliberate: a test that
// wires a source and calls something it did not expect should fail loudly rather
// than install an empty mod and pass.
type stubSource struct {
	creds    domain.AuthCredentials
	id       string
	searches []domain.SearchQuery
	asked    []string
	mu       sync.Mutex
}

func (s *stubSource) ID() string          { return s.id }
func (s *stubSource) Name() string        { return "Stub Source" }
func (s *stubSource) Description() string { return "a source for tests" }
func (s *stubSource) Version() string     { return "1.0.0" }

func (s *stubSource) SupportsSearch() bool { return true }
func (s *stubSource) SupportsAuth() bool   { return true }
func (s *stubSource) RequiresAPIKey() bool { return false }

func (s *stubSource) Search(ctx context.Context, query domain.SearchQuery) ([]domain.Mod, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.searches = append(s.searches, query)

	return []domain.Mod{}, nil
}

func (s *stubSource) GetMod(ctx context.Context, modID string) (*domain.Mod, error) {
	s.mu.Lock()
	s.asked = append(s.asked, modID)
	s.mu.Unlock()

	return nil, appErrors.New(appErrors.ErrCodeModNotFound, "no mod %q", modID)
}

func (s *stubSource) Download(
	ctx context.Context,
	mod *domain.Mod,
	destPath string,
) (string, error) {
	return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "stub sources do not download")
}

func (s *stubSource) Authenticate(ctx context.Context, creds domain.AuthCredentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.creds = creds

	return nil
}

func (s *stubSource) ValidateCredentials(ctx context.Context) error { return nil }

// lastSearch returns the most recent query the source was asked about.
func (s *stubSource) lastSearch() (domain.SearchQuery, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.searches) == 0 {
		return domain.SearchQuery{}, false
	}

	return s.searches[len(s.searches)-1], true
}

// lastCreds returns the credentials the source was last given.
func (s *stubSource) lastCreds() domain.AuthCredentials {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.creds
}

// noSearchSource is a source that cannot be searched, for the branch where
// SearchSource has to say so rather than return an empty list.
type noSearchSource struct{ stubSource }

func (s *noSearchSource) SupportsSearch() bool { return false }

// failingSource refuses every operation it is asked to perform, with a code the
// test chooses.
type failingSource struct {
	code appErrors.Code
	stubSource
}

func (s *failingSource) Search(
	ctx context.Context,
	query domain.SearchQuery,
) ([]domain.Mod, error) {
	return nil, appErrors.New(s.code, "stub failure")
}

func (s *failingSource) Authenticate(ctx context.Context, creds domain.AuthCredentials) error {
	return appErrors.New(s.code, "stub failure")
}

func (s *failingSource) ValidateCredentials(ctx context.Context) error {
	return appErrors.New(s.code, "stub failure")
}

var (
	_ domain.ModSource = (*stubSource)(nil)
	_ domain.ModSource = (*noSearchSource)(nil)
	_ domain.ModSource = (*failingSource)(nil)
)

// hooksRegisterForTest wires the real hook runners onto an executor. It is a
// helper rather than an import at the top of the file so that a test which does
// not care about hooks does not drag the runners into its binary — the golang
// runner in particular registers a compiled-in function under a build tag.
func hooksRegisterForTest(executor *services.HookExecutor) error {
	return hooks.RegisterAll(executor)
}
