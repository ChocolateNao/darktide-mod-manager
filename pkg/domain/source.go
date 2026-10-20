package domain

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// ModSource is the plugin contract every mod provider implements. A generic
// factory (RegisterSourceFactory) is visible to every game; a game-scoped one
// (RegisterGameSourceFactory) only while its game is active. Scope belongs to the
// factory.
type ModSource interface {
	ID() string
	Name() string
	Description() string
	Version() string

	SupportsSearch() bool
	SupportsAuth() bool
	RequiresAPIKey() bool

	Search(ctx context.Context, query SearchQuery) ([]Mod, error)
	GetMod(ctx context.Context, modID string) (*Mod, error)
	// Download writes the mod's payload below destPath and returns the path of
	// the downloaded file.
	Download(ctx context.Context, mod *Mod, destPath string) (string, error)
	Authenticate(ctx context.Context, credentials AuthCredentials) error
	ValidateCredentials(ctx context.Context) error
}

// GameScopedSource lets a caller ask which game a source belongs to. It is
// optional: the factory's registration scope already decides visibility.
type GameScopedSource interface {
	GameID() string
}

// CredentialForgetter is implemented by sources that can drop the credential they
// hold. It is optional: a folder of mods on disk has nothing to forget.
//
// Deleting a key is two-part: clearing the settings file stops it coming back on
// the next launch, but the source built in this process would keep sending it.
type CredentialForgetter interface {
	// ForgetCredentials drops the held credential without touching the network: a
	// call that can fail because a launcher is down cannot promise the key is gone.
	ForgetCredentials()
}

// PathInspector is implemented by sources that read mods off the filesystem. It is
// optional for the same reason CredentialForgetter is, and the manager needs it
// because a drag gesture hands over a path.
//
// The boolean means "this source recognises the path": false asks the next source
// and must come with no error; true means the Mod is the answer.
type PathInspector interface {
	// InspectPath describes the mod at path, or reports that it does not recognise
	// it.
	InspectPath(ctx context.Context, path string) (mod *Mod, recognised bool, err error)
}

// SearchQuery is the provider-agnostic search request. Page is 1-based; PageSize
// of zero means "provider default".
type SearchQuery struct {
	Query       string   `json:"query"`
	GameVersion string   `json:"game_version,omitempty"`
	SortBy      string   `json:"sort_by,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Page        int      `json:"page,omitempty"`
	PageSize    int      `json:"page_size,omitempty"`
}

// Sort orders for provider searches. Providers may support a subset.
const (
	SortRelevance = "relevance"
	SortUpdated   = "updated"
	SortDownloads = "downloads"
	SortName      = "name"
)

// Normalized returns a copy of the query with a 1-based page, a positive page size
// and a default sort, so providers never defend against zero.
func (q SearchQuery) Normalized() SearchQuery {
	out := q
	if out.Page < 1 {
		out.Page = 1
	}

	if out.PageSize < 1 {
		out.PageSize = 20
	}

	if out.SortBy == "" {
		out.SortBy = SortRelevance
	}

	return out
}

// AuthCredentials is an open credential bag: providers pick what they need
// (api_key, username, password, token). Keys are lower case by convention.
type AuthCredentials map[string]string

// Get returns the credential for key, trimmed of surrounding whitespace, and
// reports whether it was present and non-empty.
func (c AuthCredentials) Get(key string) (string, bool) {
	if c == nil {
		return "", false
	}

	v, ok := c[key]
	if !ok {
		return "", false
	}

	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}

	return v, true
}

func (c AuthCredentials) Has(key string) bool {
	_, ok := c.Get(key)
	return ok
}

// SearchAll fans a query out across sources and merges results. An error is
// returned only when every provider failed, and it is the first failure.
func SearchAll(ctx context.Context, sources []ModSource, query SearchQuery) ([]Mod, error) {
	if len(sources) == 0 {
		return nil, nil
	}

	type result struct {
		err  error
		mods []Mod
	}

	results := make([]result, len(sources))

	var wg sync.WaitGroup

	for i, src := range sources {
		if src == nil || !src.SupportsSearch() {
			continue
		}

		wg.Add(1)
		go func(i int, src ModSource) {
			defer wg.Done()

			mods, err := src.Search(ctx, query.Normalized())
			results[i] = result{mods: mods, err: err}
		}(i, src)
	}

	wg.Wait()

	merged := make([]Mod, 0, len(sources))

	var firstErr error

	failures := 0
	attempted := 0

	for _, res := range results {
		if res.mods == nil && res.err == nil {
			continue
		}

		attempted++

		if res.err != nil {
			failures++

			if firstErr == nil {
				firstErr = res.err
			}

			continue
		}

		merged = append(merged, res.mods...)
	}

	if attempted > 0 && failures == attempted {
		return nil, firstErr
	}

	return merged, nil
}

// SortMods orders mods in place according to sortBy, with ID as a stable
// tiebreaker so that repeated calls produce identical output.
func SortMods(mods []Mod, sortBy string) {
	less := func(i, j int) bool { return mods[i].ID < mods[j].ID }

	switch sortBy {
	case SortUpdated:
		less = func(i, j int) bool {
			if !mods[i].UpdatedAt.Equal(mods[j].UpdatedAt) {
				return mods[i].UpdatedAt.After(mods[j].UpdatedAt)
			}

			return mods[i].ID < mods[j].ID
		}
	case SortName:
		less = func(i, j int) bool {
			li, lj := strings.ToLower(mods[i].Name), strings.ToLower(mods[j].Name)
			if li != lj {
				return li < lj
			}

			return mods[i].ID < mods[j].ID
		}
	case SortDownloads:
		less = func(i, j int) bool {
			di, dj := downloadCount(mods[i]), downloadCount(mods[j])
			if di != dj {
				return di > dj
			}

			return mods[i].ID < mods[j].ID
		}
	}

	sort.SliceStable(mods, less)
}

func downloadCount(m Mod) float64 {
	switch v := m.SourceData["download_count"].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case uint64:
		return float64(v)
	default:
		return 0
	}
}
