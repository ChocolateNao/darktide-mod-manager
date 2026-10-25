// Package local implements the "local folder" mod source: a directory tree the user
// points at, containing mod folders or mod archives.
//
// It is the only source needing no network and no credentials, which makes it both
// the offline fallback and the easiest way to develop a mod against the manager. It
// reads the manifests internal/manifest describes, so a mod laid out by hand behaves
// like one downloaded from Nexus and the format has one owner.
package local

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	manifestpkg "github.com/ChocolateNao/darktide-mod-manager/internal/manifest"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// SourceID is the stable identifier used in configuration and on the command line.
const SourceID = "local"

// init registers the constructor. Nothing imports local for its side effects
// otherwise, so a missing line here leaves the manager with no local source and
// nothing failing to compile; building *is* deferred to the factory, for the
// reason explained in RegisterAll. No paths is not an error — the source stays
// listed, with its own ValidateCredentials reporting that it has nothing to scan.
func init() {
	_ = services.RegisterSourceFactory(
		SourceID,
		func(ctx services.SourceBuildContext) (domain.ModSource, error) {
			return New(ctx.Config.Paths), nil
		},
	)
}

// pluginVersion is reported through ModSource.Version.
const pluginVersion = "1.0.0"

// defaultMaxScanDepth bounds how deep Search walks a configured path. A user
// pointing at their home directory should not make the manager stat a million
// files; real mod layouts are one or two folders deep.
const defaultMaxScanDepth = 4

// maxScannedMods bounds how many mods one scan produces. The result is sorted, so a
// truncated scan still returns the same mods on every run, which is what makes the
// pagination below coherent.
const maxScannedMods = 5000

// Source is the local-folder mod provider.
type Source struct {
	paths []string
	// maxDepth is the scan depth; zero means defaultMaxScanDepth. It is settable
	// so a test (or a future "deep scan" toggle) does not need a real tree.
	maxDepth int
}

// Option customises a Source.
type Option func(*Source)

// WithPaths replaces the configured search paths.
func WithPaths(paths []string) Option {
	return func(s *Source) { s.paths = append([]string(nil), paths...) }
}

// WithMaxDepth sets the directory depth Search descends to.
func WithMaxDepth(depth int) Option {
	return func(s *Source) { s.maxDepth = depth }
}

// New returns a Source reading the given folders. Blank entries are dropped so a
// trailing comma in a config array does not become a "." scan of the working
// directory.
func New(paths []string, opts ...Option) *Source {
	s := &Source{}

	for _, p := range paths {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			s.paths = append(s.paths, trimmed)
		}
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// ID implements domain.ModSource.
func (s *Source) ID() string { return SourceID }

// Name implements domain.ModSource.
func (s *Source) Name() string { return "Local folders" }

// Description implements domain.ModSource.
func (s *Source) Description() string {
	return "Mods already on this computer, discovered from folders you configure"
}

// Version implements domain.ModSource.
func (s *Source) Version() string { return pluginVersion }

// SupportsSearch implements domain.ModSource. The local source always can: a
// folder scan is a search.
func (s *Source) SupportsSearch() bool { return true }

// SupportsAuth implements domain.ModSource.
func (s *Source) SupportsAuth() bool { return false }

// RequiresAPIKey implements domain.ModSource.
func (s *Source) RequiresAPIKey() bool { return false }

// Authenticate implements domain.ModSource. It is a no-op: the local source has
// nothing to authenticate against, and returning an error would make the UI show
// a credential prompt that cannot succeed.
func (s *Source) Authenticate(context.Context, domain.AuthCredentials) error { return nil }

// ValidateCredentials implements domain.ModSource. A nil error means "the source
// is usable", which for a folder scan means only that paths are configured.
func (s *Source) ValidateCredentials(context.Context) error {
	if len(s.paths) == 0 {
		return appErrors.New(appErrors.ErrCodeSourceUnavailable,
			"no local folders configured for source %q", SourceID)
	}

	return nil
}

// entry is one discovered mod: the folder that holds the payload plus its
// manifest. Keeping the folder means Download can copy without re-reading it.
type entry struct {
	manifest   *manifestpkg.Manifest
	dir        string
	fallbackID string
	archived   bool
}

// mod turns a discovered entry into the domain model. Read has
// already validated the hook languages, so the error path is a safety net for
// entries built in memory.
func (e entry) mod() (*domain.Mod, error) {
	// InstalledAt is left zero on purpose: the payload was already on disk, so there
	// is no instant at which the manager installed it.
	m := &domain.Mod{
		ID:        e.fallbackID,
		SourceID:  SourceID,
		LocalPath: e.dir,
		// The cache key the mod service derives hashes the download location, and a
		// local mod has no URL, so the payload path is the only thing there is to hash.
		SourceData: map[string]any{"local_path": e.dir},
	}
	if e.manifest == nil {
		m.Name = e.fallbackID
		return m, nil
	}

	hooks, err := e.manifest.DomainHooks()
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidMod,
			"mod %s has an invalid hook entry", e.fallbackID)
	}

	m.Hooks = hooks
	if id := strings.TrimSpace(e.manifest.Mod.ID); id != "" {
		m.ID = id
	}

	m.Name = firstNonBlank(e.manifest.Mod.Name, m.ID)
	m.Version = firstNonBlank(e.manifest.Mod.Version, "0.0.0")
	m.Author = strings.TrimSpace(e.manifest.Mod.Author)
	m.Description = strings.TrimSpace(e.manifest.Mod.Description)
	m.GameVersion = strings.TrimSpace(e.manifest.Mod.GameVersion)
	m.Dependencies = manifestpkg.TrimmedList(e.manifest.Mod.Dependencies)
	m.ConflictsWith = manifestpkg.TrimmedList(e.manifest.Mod.ConflictsWith)
	m.Tags = manifestpkg.TrimmedList(e.manifest.Mod.Tags)

	return m, nil
}

// Search implements domain.ModSource.
func (s *Source) Search(ctx context.Context, query domain.SearchQuery) ([]domain.Mod, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled, "local search canceled")
	}

	entries, err := s.discover(ctx)
	if err != nil {
		return nil, err
	}

	normalized := query.Normalized()

	matches := make([]domain.Mod, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, appErrors.Wrap(
				err,
				appErrors.ErrCodeOperationCanceled,
				"local search canceled",
			)
		}

		m, err := e.mod()
		if err != nil {
			// Read already skipped manifests that failed validation, so anything left
			// here is per-entry and must not hide the rest of the results.
			continue
		}

		if !matchesQuery(m, normalized) {
			continue
		}

		matches = append(matches, *m)
	}

	domain.SortMods(matches, normalized.SortBy)

	return paginate(matches, normalized), nil
}

// GetMod implements domain.ModSource.
func (s *Source) GetMod(ctx context.Context, modID string) (*domain.Mod, error) {
	id := strings.TrimSpace(modID)
	if id == "" {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "empty mod id")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	entries, err := s.discover(ctx)
	if err != nil {
		return nil, err
	}
	// A mod id has to be unique across all configured paths: two folders claiming one
	// would let install order decide which payload wins, so the collision is
	// reported instead of silently picking the first.
	var (
		found     *domain.Mod
		collision string
	)

	for _, e := range entries {
		m, err := e.mod()
		if err != nil {
			return nil, err
		}

		if !strings.EqualFold(m.ID, id) {
			continue
		}

		if found != nil {
			collision = e.dir
			break
		}

		found = m
	}

	if found == nil {
		return nil, appErrors.New(appErrors.ErrCodeModNotFound,
			"mod %q not found in the configured local folders", id)
	}

	if collision != "" {
		// The fix depends on the kind of mod: a folder reads its id from a manifest, a
		// bare archive from its file name, and advising the wrong one is useless.
		fix := "rename one of the files so their names differ"
		if info, err := os.Stat(found.LocalPath); err == nil && info.IsDir() {
			fix = "give one of them a distinct id in its mod.toml"
		}

		return nil, appErrors.New(appErrors.ErrCodeModNotFound,
			"mod %q is claimed by both %q and %q; %s",
			id, found.LocalPath, collision, fix)
	}

	return found, nil
}

// InspectPath implements domain.PathInspector. It deliberately does not consult the
// configured paths: a dropped path is whatever the user let go of, and refusing one
// outside the search roots would miss a mod downloaded elsewhere.
func (s *Source) InspectPath(ctx context.Context, path string) (*domain.Mod, bool, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, false, appErrors.New(appErrors.ErrCodeInvalidArgument, "empty path")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return nil, false, appErrors.Wrap(err, appErrors.ErrCodeOperationCanceled,
			"inspect local path canceled")
	}

	info, err := os.Stat(trimmed)
	if err != nil {
		return nil, false, appErrors.Wrap(
			err,
			appErrors.ErrCodeNotFound,
			"inspect local path %q",
			trimmed,
		)
	}

	if !info.IsDir() {
		e, ok := archiveEntry(trimmed)
		if !ok {
			return nil, false, nil
		}

		return modFromEntry(e)
	}

	e, ok := folderEntry(trimmed)
	if !ok {
		return nil, false, nil
	}

	return modFromEntry(e)
}

// folderEntry turns a dropped directory into an entry: the directory itself is the
// mod when it holds a manifest, otherwise a single mod folder inside it, since a user
// who drops their "Mods" folder meant the one thing in it. Exactly one child must
// qualify; two or more is a guess the manager should not make silently.
func folderEntry(dir string) (entry, bool) {
	if manifestpkg.Has(dir) {
		m, err := manifestpkg.Read(dir)
		if err != nil {
			// A malformed manifest is still a mod folder: the error belongs to the
			// install attempt, which can report it with better context.
			m = nil
		}

		return entry{dir: dir, manifest: m, fallbackID: fallbackID(dir)}, true
	}

	var found []entry

	children, err := os.ReadDir(dir)
	if err != nil {
		return entry{}, false
	}

	for _, child := range children {
		if !child.IsDir() || !manifestpkg.Has(filepath.Join(dir, child.Name())) {
			continue
		}

		found = append(found, entry{
			dir:        filepath.Join(dir, child.Name()),
			fallbackID: fallbackID(filepath.Join(dir, child.Name())),
		})
		if len(found) > 1 {
			return entry{}, false
		}
	}

	if len(found) != 1 {
		return entry{}, false
	}

	return found[0], true
}

// modFromEntry is the one place an entry becomes a Mod, so a dropped folder and
// a discovered one cannot drift apart in how they are described.
func modFromEntry(e entry) (*domain.Mod, bool, error) {
	m, err := e.mod()
	if err != nil {
		return nil, false, err
	}

	return m, true, nil
}

// Download implements domain.ModSource. It copies the mod folder or archive to
// destPath and returns the copied path.
func (s *Source) Download(ctx context.Context, mod *domain.Mod, destPath string) (string, error) {
	if mod == nil {
		return "", appErrors.New(appErrors.ErrCodeInvalidMod, "cannot download a nil mod")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return "", appErrors.Wrap(
			err,
			appErrors.ErrCodeOperationCanceled,
			"local download canceled",
		)
	}

	dest := strings.TrimSpace(destPath)
	if dest == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "empty download destination")
	}

	source := strings.TrimSpace(mod.LocalPath)
	if source == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"mod %q has no local path", modLabel(mod))
	}

	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return "", appErrors.Wrap(err, appErrors.ErrCodeModNotFound,
				"local payload for mod %q is gone", modLabel(mod))
		}

		return "", appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "stat %q", source)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", appErrors.Wrap(
			err,
			appErrors.ErrCodeConfigIO,
			"create download directory for %q",
			dest,
		)
	}
	// The destination is removed first so an existing symlink there cannot be
	// followed into a directory the manager does not own.
	_ = os.RemoveAll(dest)

	if info.IsDir() {
		if err := copyTree(source, dest); err != nil {
			_ = os.RemoveAll(dest)
			return "", err
		}

		return dest, nil
	}

	if err := copyFile(source, dest); err != nil {
		_ = os.RemoveAll(dest)
		return "", err
	}

	return dest, nil
}

// Verify is not part of domain.ModSource. There is nothing to verify: the payload
// came off the user's own disk over a local copy, so a checksum would only be the
// manager checking its own work.
func (s *Source) Verify(context.Context, *domain.Mod, string) error { return nil }

// discover walks every configured path and returns the mods found. A path that does
// not exist is not an error — it is a folder the user may not have created yet, and
// one missing folder must not make every other path unreachable. Every other failure
// is reported, because returning nothing would look like "no mods installed".
func (s *Source) discover(ctx context.Context) ([]entry, error) {
	if len(s.paths) == 0 {
		return nil, appErrors.New(appErrors.ErrCodeSourceUnavailable,
			"no local folders configured for source %q", SourceID)
	}

	depth := s.maxDepth
	if depth <= 0 {
		depth = defaultMaxScanDepth
	}

	var out []entry

	seen := make(map[string]bool)

	for _, root := range s.paths {
		info, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "stat local folder %q", root)
		}

		if !info.IsDir() {
			// A configured path that is an archive is treated as a single mod; a
			// file that is not a recognised archive is simply ignored.
			if e, ok := archiveEntry(root); ok {
				if key := strings.ToLower(e.dir); !seen[key] {
					seen[key] = true

					out = append(out, e)
				}
			}

			continue
		}

		found, err := scanDir(ctx, root, depth)
		if err != nil {
			return nil, err
		}

		for _, e := range found {
			key := strings.ToLower(e.dir)
			if seen[key] {
				continue
			}

			seen[key] = true

			out = append(out, e)
		}
	}
	// Deterministic order: the same configuration must produce the same list, or
	// "page 1" would show different mods on each scan.
	sort.Slice(out, func(i, j int) bool { return out[i].dir < out[j].dir })

	return out, nil
}

// scanDir walks root looking for mod folders and archives. depth bounds how many
// directory levels below root are visited, so a folder the user pointed at their home
// directory cannot turn the scan into a full-disk walk; a depth of 1 reaches root's
// immediate children only.
func scanDir(ctx context.Context, root string, depth int) ([]entry, error) {
	if depth <= 0 {
		depth = defaultMaxScanDepth
	}

	var out []entry

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory must not abort the whole scan: the
			// manager can still install everything it did find.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}

			return nil
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		if len(out) >= maxScannedMods {
			return fs.SkipAll
		}

		if path != root && !withinDepth(root, path, depth) {
			if d.IsDir() {
				// Nothing below this point can be within the depth budget.
				return fs.SkipDir
			}

			return nil
		}

		if d.IsDir() {
			// The root itself is a mod folder if it holds a manifest.
			if path != root && manifestpkg.Has(path) {
				m, err := manifestpkg.Read(path)
				if err != nil {
					// A malformed manifest is skipped and not failing the scan:
					// one broken mod should not make the others invisible.
					return fs.SkipDir
				}

				out = append(out, entry{dir: path, manifest: m, fallbackID: fallbackID(path)})

				return fs.SkipDir
			}

			return nil
		}

		if e, ok := archiveEntry(path); ok {
			out = append(out, e)
		}

		return nil
	})
	if err != nil {
		// WalkDir wraps the sentinel used to stop early; those are not failures.
		if errors.Is(err, fs.SkipAll) || errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, appErrors.Wrap(
					ctxErr,
					appErrors.ErrCodeOperationCanceled,
					"local scan canceled",
				)
			}

			return out, nil
		}

		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "scan local folder %q", root)
	}

	return out, nil
}

// withinDepth reports whether path sits no deeper than depth directory levels
// below root. root itself is always within the budget.
func withinDepth(root, path string, depth int) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		// Rel only fails when one path is relative and the other absolute, which
		// WalkDir cannot produce. Refusing to descend is the safe answer.
		return false
	}

	if rel == "." {
		return true
	}

	levels := 0

	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part != "" && part != "." {
			levels++
		}
	}

	return levels <= depth
}

// archiveExtensions are the archive types a folder scan treats as a mod.
var archiveExtensions = map[string]bool{
	".zip": true, ".jar": true, ".mod": true, ".pak": true,
	".tgz": true, ".tar.gz": true, ".tar": true,
}

// archiveEntry reports whether path is an archive this source can install, and
// returns the entry for it.
func archiveEntry(path string) (entry, bool) {
	lower := strings.ToLower(path)
	matched := false

	for ext := range archiveExtensions {
		if strings.HasSuffix(lower, ext) {
			matched = true
			break
		}
	}

	if !matched {
		return entry{}, false
	}

	base := filepath.Base(lower)
	for ext := range archiveExtensions {
		if strings.HasSuffix(base, ext) {
			base = strings.TrimSuffix(base, ext)
			break
		}
	}
	// "1.2.0" style suffixes are how mod authors name release files, and dropping
	// them would give every version of a mod the same id and folder.
	if idx := strings.LastIndex(base, " "); idx > 0 {
		if looksLikeVersion(strings.TrimSpace(base[idx+1:])) {
			base = strings.TrimSpace(base[:idx])
		}
	}

	if base == "" {
		return entry{}, false
	}

	return entry{
		dir:        path,
		archived:   true,
		fallbackID: SanitizeID(base),
		manifest:   &manifestpkg.Manifest{},
	}, true
}

// SanitizeID turns a folder or file name into a mod id: the character set
// domain.SanitizeSegment allows, plus lower-casing. Local ids come from names that
// routinely contain spaces, slashes and mixed case, so the restricted set keeps the id
// usable as one path segment. Lower-casing on top of SanitizeSegment (which preserves
// case) makes folders differing only in case collide as ids on every platform, so
// ModService reports a duplicate-id conflict instead of the user finding the clash.
func SanitizeID(name string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return -1
		}
	}, strings.TrimSpace(name))

	out = strings.Trim(out, ".")
	if out == "" {
		return "local_mod"
	}

	return out
}

// matchesQuery reports whether m satisfies the search query.
func matchesQuery(m *domain.Mod, q domain.SearchQuery) bool {
	if want := strings.TrimSpace(q.Query); want != "" {
		haystack := strings.ToLower(strings.Join([]string{
			m.ID, m.Name, m.Description, m.Author,
		}, "\n"))
		if !strings.Contains(haystack, strings.ToLower(want)) {
			return false
		}
	}

	for _, want := range q.Tags {
		trimmed := strings.TrimSpace(want)
		if trimmed == "" {
			continue
		}

		if !m.HasTag(trimmed) {
			return false
		}
	}

	if want := strings.TrimSpace(
		q.GameVersion,
	); want != "" &&
		!satisfiesGameVersion(m.GameVersion, want) {
		return false
	}

	return true
}

// satisfiesGameVersion reports whether the mod declares compatibility with the wanted
// game version. A mod that declares none is treated as compatible: the field is
// optional, and refusing it would make every minimally-specified local mod invisible.
func satisfiesGameVersion(declared, wanted string) bool {
	declared = strings.TrimSpace(declared)
	if declared == "" {
		return true
	}

	return domain.SatisfiesExpression(wanted, declared)
}

// paginate applies the query's 1-based page and page size. Empty input comes back
// untouched, so a caller passing an empty slice gets it back unchanged.
func paginate(mods []domain.Mod, q domain.SearchQuery) []domain.Mod {
	if len(mods) == 0 {
		return mods
	}

	start := (q.Page - 1) * q.PageSize
	if start >= len(mods) {
		return []domain.Mod{}
	}

	end := start + q.PageSize
	if end > len(mods) {
		end = len(mods)
	}

	return mods[start:end]
}
