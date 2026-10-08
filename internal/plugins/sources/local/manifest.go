package local

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// manifest is the on-disk shape of mod.toml.
//
// Every field is optional: a manifest that only sets an id is a valid mod, and a
// mod folder with no manifest at all is discovered under its folder name. That
// leniency is the point of a local source — mod authors are not going to write
// perfect manifests, and a source that rejected incomplete ones would mostly
// report "nothing found".
type manifest struct {
	Mod struct {
		ID            string   `toml:"id"`
		Name          string   `toml:"name"`
		Version       string   `toml:"version"`
		Author        string   `toml:"author"`
		Description   string   `toml:"description"`
		GameVersion   string   `toml:"game_version"`
		Dependencies  []string `toml:"dependencies"`
		ConflictsWith []string `toml:"conflicts_with"`
		Tags          []string `toml:"tags"`
	} `toml:"mod"`
	// Hooks are read as arrays because the manifest uses [[hooks.pre_enable]]
	// notation; BurntSushi decodes a repeated table into a slice only when the
	// destination field is one.
	Hooks struct {
		PreInstall    []hookEntry `toml:"pre_install"`
		PostInstall   []hookEntry `toml:"post_install"`
		PreEnable     []hookEntry `toml:"pre_enable"`
		PostEnable    []hookEntry `toml:"post_enable"`
		PreDisable    []hookEntry `toml:"pre_disable"`
		PostDisable   []hookEntry `toml:"post_disable"`
		PreUninstall  []hookEntry `toml:"pre_uninstall"`
		PostUninstall []hookEntry `toml:"post_uninstall"`
		PreUpdate     []hookEntry `toml:"pre_update"`
		PostUpdate    []hookEntry `toml:"post_update"`
	} `toml:"hooks"`
}

// hookEntry is one script in a manifest.
type hookEntry struct {
	Type       string   `toml:"type"`
	Content    string   `toml:"content"`
	WorkingDir string   `toml:"working_dir"`
	Env        []string `toml:"env"`
	Timeout    int      `toml:"timeout"`
}

// script returns the domain script for this manifest entry.
//
// A missing type defaults to `inline`, which is what the shortest possible
// manifest script means. An unrecognised type is normalised to `inline` as well:
// the hook executor refuses unknown types outright, and a typo in a manifest
// should not make the mod impossible to enable. Blank content yields a zero
// script, which the caller drops.
func (e hookEntry) script() domain.HookScript {
	kind := domain.HookType(strings.TrimSpace(e.Type))
	if !kind.Valid() {
		kind = domain.HookTypeInline
	}

	return domain.HookScript{
		Type:       kind,
		Content:    e.Content,
		Timeout:    e.Timeout,
		WorkingDir: strings.TrimSpace(e.WorkingDir),
		Env:        trimmedList(e.Env),
	}
}

// hookPhaseEntries pairs each manifest hook table with its domain phase. It is
// written out in the same order as domain.HookPhases so adding a phase to the
// domain model without a manifest mapping shows up as a zero field here rather
// than as a silently ignored manifest table.
func (m *manifest) hookPhaseEntries() []struct {
	Phase   domain.HookPhase
	Entries []hookEntry
} {
	if m == nil {
		return nil
	}

	return []struct {
		Phase   domain.HookPhase
		Entries []hookEntry
	}{
		{domain.PhasePreInstall, m.Hooks.PreInstall},
		{domain.PhasePostInstall, m.Hooks.PostInstall},
		{domain.PhasePreEnable, m.Hooks.PreEnable},
		{domain.PhasePostEnable, m.Hooks.PostEnable},
		{domain.PhasePreDisable, m.Hooks.PreDisable},
		{domain.PhasePostDisable, m.Hooks.PostDisable},
		{domain.PhasePreUninstall, m.Hooks.PreUninstall},
		{domain.PhasePostUninstall, m.Hooks.PostUninstall},
		{domain.PhasePreUpdate, m.Hooks.PreUpdate},
		{domain.PhasePostUpdate, m.Hooks.PostUpdate},
	}
}

// Hooks converts the manifest's hook tables into the domain model.
//
// A script with blank content is dropped rather than stored: the hook executor
// rejects blank content, so keeping it would make an otherwise-working mod fail
// to enable over a manifest line that carries no information.
func (m *manifest) DomainHooks() domain.ModHooks {
	var out domain.ModHooks

	for _, pair := range m.hookPhaseEntries() {
		if len(pair.Entries) == 0 {
			continue
		}

		scripts := make([]domain.HookScript, 0, len(pair.Entries))
		for _, e := range pair.Entries {
			if strings.TrimSpace(e.Content) == "" {
				continue
			}

			scripts = append(scripts, e.script())
		}

		if len(scripts) > 0 {
			out.Set(pair.Phase, scripts)
		}
	}

	return out
}

// hasManifestDir reports whether dir directly contains one of the recognised
// manifest files.
func hasManifestDir(dir string) bool {
	for _, name := range manifestNames {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
	}

	return false
}

// readManifest parses the manifest of a mod folder. The first recognised manifest
// file wins, matching the deployer's own priority order.
func readManifest(dir string) (*manifest, error) {
	for _, name := range manifestNames {
		path := filepath.Join(dir, name)

		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read manifest %q", path)
		}

		var m manifest
		if err := toml.Unmarshal(data, &m); err != nil {
			return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidMod,
				"parse manifest %q: %v", path, err)
		}

		return &m, nil
	}
	// No manifest at all is legal for a local mod; the folder name is the id.
	return &manifest{}, nil
}

// fallbackID derives a mod id from a folder name.
func fallbackID(dir string) string {
	return SanitizeID(filepath.Base(filepath.Clean(dir)))
}

// trimmedList drops blank entries from a manifest list and trims the rest.
func trimmedList(in []string) []string {
	if len(in) == 0 {
		return nil
	}

	out := make([]string, 0, len(in))
	for _, v := range in {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// firstNonBlank returns the first non-blank value trimmed, or "".
func firstNonBlank(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}

	return ""
}

// modLabel renders a mod for an error message without dereferencing nil.
func modLabel(mod *domain.Mod) string {
	if mod == nil || strings.TrimSpace(mod.ID) == "" {
		return "<unknown>"
	}

	return mod.ID
}

// looksLikeVersion reports whether s is a dotted numeric version such as "1.2.0".
func looksLikeVersion(s string) bool {
	if s == "" {
		return false
	}

	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}

		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}

	return true
}

// copyTree copies a folder tree, refusing to follow a symlink out of it.
//
// A local mod folder is user-owned, so links inside it are expected; a link whose
// target escapes the tree would let a "copy" read arbitrary files and then index
// them as mod content. Such an entry is skipped and reported rather than copied.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read %q", path)
		}

		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return appErrors.Wrap(
				relErr,
				appErrors.ErrCodeConfigIO,
				"resolve %q under %q",
				path,
				src,
			)
		}

		target := filepath.Join(dst, rel)

		switch {
		case d.IsDir():
			if err := os.MkdirAll(target, 0o750); err != nil {
				return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "create %q", target)
			}

			return nil
		case d.Type()&fs.ModeSymlink != 0:
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "resolve link %q", path)
			}

			within, err := filepath.Rel(src, resolved)
			if err != nil || within == ".." ||
				strings.HasPrefix(within, ".."+string(filepath.Separator)) {
				return appErrors.New(appErrors.ErrCodeInvalidMod,
					"link %q points outside the mod folder and was not copied", rel)
			}

			return copyFile(resolved, target)
		case !d.Type().IsRegular():
			// Sockets, devices and FIFOs cannot appear in a mod archive anyway.
			return nil
		default:
			return copyFile(path, target)
		}
	})
}

// copyFile copies one file, preserving its permission bits. The destination must
// not exist: the caller has already removed any pre-existing entry.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "open %q", src)
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "stat %q", src)
	}

	if info.IsDir() {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "%q is a directory", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "create %q", filepath.Dir(dst))
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "create %q", dst)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "copy %q", src)
	}

	if err := out.Close(); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "close %q", dst)
	}

	return nil
}
