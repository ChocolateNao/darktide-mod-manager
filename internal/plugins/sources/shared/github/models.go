package github

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/sourceutil"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// release is the subset of the GitHub release payload this source reads. Fields
// the manager does not use are deliberately absent, so an upstream rename fails
// loudly here instead of becoming a zero value somewhere mid-manager.
type release struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	Body        string  `json:"body"`
	PublishedAt string  `json:"published_at"`
	HTMLURL     string  `json:"html_url"`
	Assets      []asset `json:"assets"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
}

// asset is one file attached to a release.
type asset struct {
	Name               string `json:"name"`
	ContentType        string `json:"content_type"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

// archiveExtensions are the release asset suffixes treated as a mod payload;
// everything else in a release is metadata, not something to install.
var archiveExtensions = []string{".zip", ".tar.gz", ".tgz", ".tar.bz2", ".rar", ".7z"}

// validRepoRef reports whether ref looks like "owner/repo": exactly one slash, with
// a non-blank name on either side. The check exists because the value goes straight
// into a URL path.
func validRepoRef(ref string) bool {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return false
	}

	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 {
		return false
	}

	return strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != ""
}

// splitRef returns the owner and repository of a reference, or a pair of empty
// strings when the reference is not "owner/repo". The value can come straight out
// of a mod id in the database, so this returns empties, not indexing past
// the end of the split.
func splitRef(ref string) (owner, repo string) {
	parts := strings.Split(strings.TrimSpace(ref), "/")
	if len(parts) != 2 {
		return "", ""
	}

	owner = strings.TrimSpace(parts[0])

	repo = strings.TrimSpace(parts[1])
	if owner == "" || repo == "" {
		return "", ""
	}

	return owner, repo
}

// ModID renders a repository reference as a mod id, and is the exact inverse of
// repoFromModID. The separator is a dot so "someone/darktide.mods" and
// "someone/darktide" stay distinct, and the id survives domain.SanitizeSegment as
// one path segment. Dots are stripped from the owner for the same reason: GitHub
// logins contain none, but without it ModID("some.one", "pack") reverses to the
// different repository "some/one.pack".
func ModID(owner, repo string) string {
	owner = strings.TrimSpace(owner)

	repo = strings.TrimSpace(repo)
	if owner == "" {
		return repo
	}

	if repo == "" {
		return owner
	}

	return strings.ReplaceAll(owner, ".", "") + "." + repo
}

// repoFromModID reverses ModID.
func repoFromModID(modID string) (string, error) {
	trimmed := strings.TrimSpace(modID)

	idx := strings.IndexByte(trimmed, '.')
	if idx <= 0 || idx == len(trimmed)-1 {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"%q is not a GitHub mod id (want owner.repository)", modID)
	}

	owner := trimmed[:idx]

	repo := trimmed[idx+1:]
	if strings.ContainsAny(repo, "/\\") || strings.ContainsAny(owner, "/\\") {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"%q is not a GitHub mod id (want owner.repository)", modID)
	}

	return owner + "/" + repo, nil
}

// normalizeVersion turns a tag into a version string. GitHub tags are unconstrained
// text, so the common "v1.2.3" prefix is stripped; anything still not version-shaped
// is kept verbatim, because a maintainer is entitled to tag a release "hotfix-2024-01".
func normalizeVersion(tag string) string {
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return ""
	}

	trimmed = strings.TrimPrefix(trimmed, "refs/tags/")
	if len(trimmed) > 1 && (trimmed[0] == 'v' || trimmed[0] == 'V') {
		rest := trimmed[1:]
		if len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
			trimmed = rest
		}
	}

	return strings.TrimSpace(trimmed)
}

// payloadAsset picks the release asset that holds the mod. When several assets
// look installable the largest wins, which is the mod archive, not an
// accompanying patch or a debug build.
func payloadAsset(assets []asset) (asset, bool) {
	var best asset

	found := false

	for _, a := range assets {
		if !isArchiveName(a.Name) {
			continue
		}

		if !found || a.Size > best.Size {
			best = a
			found = true
		}
	}

	return best, found
}

// isArchiveName reports whether name has a known archive suffix. The comparison
// is case-insensitive because release assets are named by hand.
func isArchiveName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, ext := range archiveExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}

	return false
}

// modsFor maps releases onto mods, one mod per release so that every published
// version stays installable.
func modsFor(ref string, releases []release) []domain.Mod {
	out := make([]domain.Mod, 0, len(releases))
	for _, r := range releases {
		if m, ok := modFor(ref, r); ok {
			out = append(out, m)
		}
	}

	return out
}

// modFor maps one release onto a mod.
func modFor(ref string, r release) (domain.Mod, bool) {
	version := normalizeVersion(r.TagName)
	if version == "" {
		version = normalizeVersion(r.Name)
	}

	if version == "" {
		return domain.Mod{}, false
	}

	owner, repo := splitRef(ref)
	m := domain.Mod{
		ID:          ModID(owner, repo),
		Name:        firstNonBlank(r.Name, repo, ref),
		Version:     version,
		Author:      owner,
		Description: r.Body,
		SourceID:    SourceID,
		Tags:        []string{"github", strings.ToLower(owner)},
		SourceData:  map[string]any{},
	}
	// A release named after its own repository adds nothing over the id, so an
	// empty body stays empty, not becoming placeholder text.
	if asset, ok := payloadAsset(r.Assets); ok {
		m.SourceData["download_url"] = asset.BrowserDownloadURL
		if asset.Digest != "" {
			m.SourceData["sha256"] = strings.TrimPrefix(asset.Digest, "sha256:")
		}

		if asset.Size > 0 {
			m.SourceData["size_bytes"] = asset.Size
		}
	}

	if r.HTMLURL != "" {
		m.SourceData["release_url"] = r.HTMLURL
	}

	if r.PublishedAt != "" {
		m.SourceData["published_at"] = r.PublishedAt
	}

	if r.Prerelease {
		m.SourceData["prerelease"] = true
	}
	// The release page is the mod's canonical home, so it becomes both the source
	// and the update URL; a release that names none falls back to the repo below.
	if r.HTMLURL != "" {
		m.SourceURL = r.HTMLURL
		m.UpdateURL = r.HTMLURL
	}

	if owner != "" && repo != "" {
		repoURL := "https://github.com/" + owner + "/" + repo

		m.SourceData["repo_url"] = repoURL
		if m.SourceURL == "" {
			m.SourceURL = repoURL + "/releases/latest"
			m.UpdateURL = repoURL + "/releases/latest"
		}
	}

	if du, ok := m.SourceData["download_url"].(string); ok && du != "" {
		m.DownloadURL = du
	}

	return m, true
}

// searchQuery builds the GitHub repository search expression. The topic is
// always appended, so a user query cannot widen the search to all of GitHub by
// omitting it.
func searchQuery(query, topic string) string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "topic:" + topic
	}

	return trimmed + " topic:" + topic
}

// The helpers below are thin wrappers over internal/plugins/sources/shared/sourceutil,
// kept so this package's call sites read without the package qualifier on every use.

func selectMods(mods []domain.Mod, q domain.SearchQuery) []domain.Mod {
	return sourceutil.SelectMods(mods, q)
}

func matchesQuery(m domain.Mod, q domain.SearchQuery) bool {
	return sourceutil.MatchesQuery(m, q)
}

func paginate(mods []domain.Mod, page, pageSize int) []domain.Mod {
	return sourceutil.Paginate(mods, page, pageSize)
}

// summarize renders a handful of errors on one line for an aggregate message.
func summarize(errs []error) string { return sourceutil.Summarize(errs) }

func firstNonBlank(values ...string) string { return sourceutil.FirstNonBlank(values...) }

func optionList(raw any) []string { return sourceutil.OptionList(raw) }

func splitList(raw string) []string { return sourceutil.SplitList(raw) }

func stringOf(raw any) string { return sourceutil.StringOf(raw) }

func ctxOrBackground(ctx context.Context) context.Context { return sourceutil.CtxOrBackground(ctx) }

func ensureDir(dir string) error { return sourceutil.EnsureDir(dir) }

func fileSHA256(path string) (string, error) { return sourceutil.FileSHA256(path) }

func modLabel(mod *domain.Mod) string { return sourceutil.ModLabel(mod) }

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}

	return abs
}

func credValue(creds domain.AuthCredentials, keys ...string) string {
	return sourceutil.CredValue(creds, keys...)
}
