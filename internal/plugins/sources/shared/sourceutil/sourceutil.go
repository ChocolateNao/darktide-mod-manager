// Package sourceutil holds the behaviour every mod source plugin needs but none
// of them should own: paging, query matching, error summarising and credential
// lookup, written once so they cannot drift between sources.
package sourceutil

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// DefaultPageSize is the page size used when a query does not ask for one.
const DefaultPageSize = 20

// SelectMods filters, sorts and pages so every source agrees on what "page 2"
// means. The steps stay separate so a caller can reuse MatchesQuery or Paginate.
func SelectMods(mods []domain.Mod, q domain.SearchQuery) []domain.Mod {
	kept := make([]domain.Mod, 0, len(mods))
	for _, m := range mods {
		if MatchesQuery(m, q) {
			kept = append(kept, m)
		}
	}

	domain.SortMods(kept, q.SortBy)

	return Paginate(kept, q.Page, q.PageSize)
}

// MatchesQuery reports whether a mod satisfies the query; an empty query matches
// everything. Tags are ANDed because listing two narrows the search, and a mod
// declaring no game version is never filtered out on that axis.
func MatchesQuery(m domain.Mod, q domain.SearchQuery) bool {
	needle := strings.ToLower(strings.TrimSpace(q.Query))
	if needle != "" {
		haystack := strings.ToLower(
			strings.Join([]string{m.ID, m.Name, m.Description, m.Author}, "\n"),
		)
		if !strings.Contains(haystack, needle) {
			return false
		}
	}

	for _, tag := range q.Tags {
		trimmed := strings.TrimSpace(tag)
		if trimmed == "" {
			continue
		}

		if !m.HasTag(trimmed) {
			return false
		}
	}

	if v := strings.TrimSpace(q.GameVersion); v != "" {
		if gv := strings.TrimSpace(
			m.GameVersion,
		); gv != "" &&
			!domain.SatisfiesExpression(m.Version, gv) {
			return false
		}
	}

	return true
}

// Paginate slices a result set. An out-of-range page yields an empty slice and
// the result is always non-nil; a short last page is fine.
func Paginate(mods []domain.Mod, page, pageSize int) []domain.Mod {
	if len(mods) == 0 {
		return []domain.Mod{}
	}

	if page < 1 {
		page = 1
	}

	if pageSize < 1 {
		pageSize = DefaultPageSize
	}

	start := (page - 1) * pageSize
	if start >= len(mods) {
		return []domain.Mod{}
	}

	end := start + pageSize
	if end > len(mods) {
		end = len(mods)
	}

	out := make([]domain.Mod, end-start)
	copy(out, mods[start:end])

	return out
}

// Summarize renders a handful of errors on one line. At most three are named,
// because a source that failed against forty repositories should not bury the
// useful part; the count of the rest is included.
func Summarize(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}

		parts = append(parts, appErrors.Message(err))
	}

	if len(parts) == 0 {
		return "no detail"
	}

	if len(parts) > 3 {
		parts = append(parts[:3:3], fmt.Sprintf("and %d more", len(parts)-3))
	}

	return strings.Join(parts, "; ")
}

// FirstNonBlank returns the first non-blank argument, untrimmed: these values
// become identifiers and display names, and trimming would change what the user
// sees.
func FirstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}

	return ""
}

// OptionList renders a config option that may arrive as a single string or a
// list; the config file round-trips through JSON, so both have to be accepted.
func OptionList(raw any) []string {
	switch v := raw.(type) {
	case nil:
		return nil
	case string:
		return SplitList(v)
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}

		return out
	default:
		return nil
	}
}

// SplitList splits a comma or newline separated value, dropping blanks: a
// trailing comma in a hand-edited config is a typo.
func SplitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})

	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if trimmed := strings.TrimSpace(f); trimmed != "" {
			out = append(out, trimmed)
		}
	}

	return out
}

// StringOf renders a SourceData value as a string, tolerating the types it can
// have after a round trip through the database: a hand-written number or bool
// comes back as float64 or bool.
func StringOf(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return strings.TrimSpace(fmt.Sprint(raw))
	}
}

// CtxOrBackground tolerates a nil context, which happens when a caller passes
// one straight through from a UI binding.
func CtxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}

// EnsureDir creates the parent directory of a download destination.
func EnsureDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "create download directory %q", dir)
	}

	return nil
}

// FileSHA256 hashes a downloaded file for Verify. A missing file is a cache
// miss, which is the distinction the cache manager acts on.
func FileSHA256(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "empty path to verify")
	}

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", appErrors.Wrap(err, appErrors.ErrCodeCacheMiss, "file %q is gone", path)
		}

		return "", appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "stat %q", path)
	}

	sum, err := platform.FileSHA256(path)
	if err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "hash %q", path)
	}

	return sum, nil
}

// ModLabel names a mod for an error message, tolerating nil.
func ModLabel(mod *domain.Mod) string {
	if mod == nil {
		return "<unknown>"
	}

	if id := strings.TrimSpace(mod.ID); id != "" {
		return id
	}

	return "<unknown>"
}

// CredValue returns the first credential present under any of keys, accepting
// both snake_case and camelCase because the config file is written by hand and
// by other tooling.
func CredValue(creds domain.AuthCredentials, keys ...string) string {
	for _, k := range keys {
		if v, ok := creds.Get(k); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}

	return ""
}
