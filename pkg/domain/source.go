package domain

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// ModSource is the plugin contract every mod provider implements. Adding a new
// provider requires nothing in the core: implement this interface and register
// a factory.
type ModSource interface {
	// ID returns the stable identifier used in config and in the CLI ("vortex").
	ID() string
	// Name returns the display name ("Vortex Mods").
	Name() string
	// Description returns a one line summary for the source picker.
	Description() string
	// Version returns the plugin implementation version.
	Version() string

	// SupportsSearch reports whether Search returns meaningful results.
	SupportsSearch() bool
	// SupportsAuth reports whether Authenticate does anything useful.
	SupportsAuth() bool
	// RequiresAPIKey reports whether the source cannot be used without credentials.
	RequiresAPIKey() bool

	// Search lists mods matching query.
	Search(ctx context.Context, query SearchQuery) ([]Mod, error)
	// GetMod fetches metadata for a single mod.
	GetMod(ctx context.Context, modID string) (*Mod, error)
	// Download writes the mod's payload below destPath and returns the path of
	// the downloaded file.
	Download(ctx context.Context, mod *Mod, destPath string) (string, error)
	// Authenticate installs credentials. It is a no-op for sources that do not
	// support authentication.
	Authenticate(ctx context.Context, credentials AuthCredentials) error
	// ValidateCredentials reports whether the currently held credentials work.
	ValidateCredentials(ctx context.Context) error
}

// CredentialForgetter is implemented by sources that can drop the credential
// they are holding.
//
// It is optional and deliberately not part of ModSource: a source that takes no
// credential — a folder of mods on disk — has nothing to forget, and forcing the
// method on it would make every plugin implement a no-op.
//
// The manager needs it because deleting a key is a two-part operation. Clearing
// the settings file stops the credential coming back on the next launch, but the
// source object built during this process keeps the key it was handed at start-up
// and would go on sending it until the app was restarted. A source that cannot
// forget is told so rather than quietly left holding a key the user deleted.
type CredentialForgetter interface {
	// ForgetCredentials drops the held credential. It must not contact the
	// network: the user asked for the key to be gone, and a call that can fail
	// because a launcher is down cannot promise that.
	ForgetCredentials()
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

// Normalized returns a copy of the query with a 1-based page and a positive page
// size, so that providers never have to defend against zero or negative values.
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

// Has reports whether key is present with a non-empty value.
func (c AuthCredentials) Has(key string) bool {
	_, ok := c.Get(key)
	return ok
}

// SearchAll fans a query out across sources and merges results. Failures from
// individual providers are tolerated: the returned slice contains everything
// that succeeded. Errors are returned only when every provider failed, and the
// first failure is reported.
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
			// Provider was skipped.
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
