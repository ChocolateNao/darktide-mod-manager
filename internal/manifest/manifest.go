// Package manifest owns the mod manifest: the mod.toml a mod folder may carry,
// the names it is allowed to have, and how it is written.
//
// Every part of the manager that touches the format goes through here. A format
// with three owners has three answers to "what does a manifest look like", and
// only one of them is true.
package manifest

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	zlog "github.com/rs/zerolog/log"
)

// Names are the manifest files recognised inside a mod folder, in priority
// order. mod.toml is what `manifest init` writes and what the documentation
// tells mod authors to use; the other two are accepted so a folder that already
// uses one of them keeps working.
var Names = []string{"mod.toml", "manifest.toml", "metadata.toml"}

// IsName reports whether name is one of the recognised manifest file names.
// The deployer uses it to decide what to leave out of the game folder, so it
// folds case: on a case-insensitive filesystem a mod author may well have
// written "Mod.TOML", and shipping that into the game folder helps nobody.
func IsName(name string) bool {
	return nameRank(name) < len(Names)
}

// nameRank is a manifest file's position in Names, or len(Names) when it is not
// one. Lower wins when two manifests sit at the same depth.
func nameRank(name string) int {
	for i, candidate := range Names {
		if strings.EqualFold(name, candidate) {
			return i
		}
	}

	return len(Names)
}

// Location is where a mod's manifest was found. Depth counts directory levels
// below the folder that was searched. Others holds every other manifest under
// that folder, so a caller can flag a payload that carries more than one.
type Location struct {
	Name   string
	Path   string
	Depth  int
	Others []string
}

// Locate finds the manifest a mod folder is read from, looking through its whole
// tree: a payload often keeps mod.toml beside the files rather than at the top.
// The file closest to dir wins, and manifests at the same depth follow Names
// order. A *directory* named mod.toml is not a manifest.
func Locate(dir string) (Location, bool) {
	type match struct {
		path  string
		name  string
		depth int
	}

	var matches []match

	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory must not hide the rest of the mod.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}

			return nil
		}

		if entry.IsDir() || !IsName(entry.Name()) {
			return nil
		}

		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}

		matches = append(matches, match{
			path:  path,
			name:  entry.Name(),
			depth: strings.Count(rel, string(filepath.Separator)),
		})

		return nil
	})

	if len(matches) == 0 {
		return Location{}, false
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].depth != matches[j].depth {
			return matches[i].depth < matches[j].depth
		}

		if left, right := nameRank(matches[i].name), nameRank(matches[j].name); left != right {
			return left < right
		}

		return matches[i].path < matches[j].path
	})

	chosen := matches[0]
	others := make([]string, 0, len(matches)-1)
	for _, other := range matches[1:] {
		others = append(others, other.path)
	}

	return Location{
		Name:   chosen.name,
		Path:   chosen.path,
		Depth:  chosen.depth,
		Others: others,
	}, true
}

// Find returns the manifest file a mod folder is actually read from, and
// whether the folder has one at all.
func Find(dir string) (name, path string, ok bool) {
	location, ok := Locate(dir)
	if !ok {
		return "", "", false
	}

	return location.Name, location.Path, true
}

// Has reports whether dir directly contains a recognised manifest file. Callers
// that treat a folder as a container need this shallow answer; Read and Find
// look through the whole tree.
func Has(dir string) bool {
	for _, candidate := range Names {
		full := filepath.Join(dir, candidate)

		info, err := os.Stat(full)
		if err == nil && !info.IsDir() {
			return true
		}
	}

	return false
}

// Read parses the manifest of a mod folder, the one closest to its root winning.
//
// Every field is optional: a manifest that only sets an id is a valid mod, and a
// folder with no manifest at all is discovered under its folder name. That
// leniency is the point — mod authors are not going to write perfect manifests,
// and a reader that rejected incomplete ones would mostly report "nothing found".
func Read(dir string) (*Manifest, error) {
	location, ok := Locate(dir)
	if !ok {
		return &Manifest{}, nil
	}

	data, err := os.ReadFile(location.Path)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read manifest %q", location.Path)
	}

	var m Manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidMod,
			"parse manifest %q: %v", location.Path, err)
	}

	// Hook languages are validated here, at parse time: a manifest naming an unknown
	// script language is a broken mod, and reporting it now keeps discovery from
	// half-loading it.
	if _, err := m.DomainHooks(); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidMod,
			"parse manifest %q: %v", location.Path, err)
	}

	return &m, nil
}

// Manifest is the on-disk shape of mod.toml.
type Manifest struct {
	Mod   ModInfo `toml:"mod"`
	Hooks Hooks   `toml:"hooks"`
}

// ModInfo is the [mod] table: who the mod is and what it needs.
type ModInfo struct {
	SourceData    map[string]string `toml:"source_data,omitempty"`
	SourceURL     string            `toml:"source_url,omitempty"`
	Version       string            `toml:"version"`
	Author        string            `toml:"author"`
	Description   string            `toml:"description"`
	GameVersion   string            `toml:"game_version"`
	ID            string            `toml:"id"`
	DownloadURL   string            `toml:"download_url,omitempty"`
	UpdateURL     string            `toml:"update_url,omitempty"`
	Name          string            `toml:"name"`
	Dependencies  []string          `toml:"dependencies"`
	ConflictsWith []string          `toml:"conflicts_with"`
	Tags          []string          `toml:"tags"`
}

// Hooks are read as arrays because the manifest uses [[hooks.pre_enable]]
// notation; BurntSushi decodes a repeated table into a slice only when the
// destination field is one.
type Hooks struct {
	PreInstall    []HookEntry `toml:"pre_install"`
	PostInstall   []HookEntry `toml:"post_install"`
	PreEnable     []HookEntry `toml:"pre_enable"`
	PostEnable    []HookEntry `toml:"post_enable"`
	PreDisable    []HookEntry `toml:"pre_disable"`
	PostDisable   []HookEntry `toml:"post_disable"`
	PreUninstall  []HookEntry `toml:"pre_uninstall"`
	PostUninstall []HookEntry `toml:"post_uninstall"`
	PreUpdate     []HookEntry `toml:"pre_update"`
	PostUpdate    []HookEntry `toml:"post_update"`
}

// HookEntry is one script in a manifest.
type HookEntry struct {
	Type       string   `toml:"type"`
	Content    string   `toml:"content"`
	WorkingDir string   `toml:"working_dir"`
	Env        []string `toml:"env"`
	Timeout    int      `toml:"timeout"`
}

// Script returns the domain script for this manifest entry.
func (e HookEntry) Script() (domain.HookScript, error) {
	lang, err := domain.ParseScriptLanguage(e.Type)
	if err != nil {
		return domain.HookScript{}, err
	}

	return domain.HookScript{
		Language:   lang,
		Content:    e.Content,
		Timeout:    e.Timeout,
		WorkingDir: strings.TrimSpace(e.WorkingDir),
		Env:        TrimmedList(e.Env),
	}, nil
}

// phasePair pairs one manifest hook table with its domain phase.
type phasePair struct {
	Phase   domain.HookPhase
	Entries []HookEntry
}

// phaseEntries pairs each manifest hook table with its domain phase. It is
// written out in the same order as domain.HookPhases so adding a phase there
// without a manifest mapping shows up as a zero field here.
func (m *Manifest) phaseEntries() []phasePair {
	if m == nil {
		return nil
	}

	return []phasePair{
		{Phase: domain.PhasePreInstall, Entries: m.Hooks.PreInstall},
		{Phase: domain.PhasePostInstall, Entries: m.Hooks.PostInstall},
		{Phase: domain.PhasePreEnable, Entries: m.Hooks.PreEnable},
		{Phase: domain.PhasePostEnable, Entries: m.Hooks.PostEnable},
		{Phase: domain.PhasePreDisable, Entries: m.Hooks.PreDisable},
		{Phase: domain.PhasePostDisable, Entries: m.Hooks.PostDisable},
		{Phase: domain.PhasePreUninstall, Entries: m.Hooks.PreUninstall},
		{Phase: domain.PhasePostUninstall, Entries: m.Hooks.PostUninstall},
		{Phase: domain.PhasePreUpdate, Entries: m.Hooks.PreUpdate},
		{Phase: domain.PhasePostUpdate, Entries: m.Hooks.PostUpdate},
	}
}

// DomainHooks converts the manifest's hook tables into the domain model.
//
// Every entry's type must parse as a script language even when its content is
// blank, because a manifest naming an unknown language is broken. A script with
// blank content is then dropped: the hook executor rejects it, so keeping it would
// make an otherwise-working mod fail to enable over an empty line.
func (m *Manifest) DomainHooks() (domain.ModHooks, error) {
	var out domain.ModHooks

	for _, pair := range m.phaseEntries() {
		if len(pair.Entries) == 0 {
			continue
		}

		scripts := make([]domain.HookScript, 0, len(pair.Entries))
		for _, e := range pair.Entries {
			script, err := e.Script()
			if err != nil {
				return domain.ModHooks{}, err
			}

			if strings.TrimSpace(script.Content) == "" {
				continue
			}

			scripts = append(scripts, script)
		}

		if len(scripts) > 0 {
			out.Set(pair.Phase, scripts)
		}
	}

	return out, nil
}

// TrimmedList drops blank entries from a manifest list and trims the rest.
// An all-blank list collapses to nil so it encodes as an absent field rather
// than as an empty array.
func TrimmedList(in []string) []string {
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

// Init writes a blank manifest into the mod folder at dir and returns the path it
// wrote, creating the folder if needed.
//
// An existing manifest is never overwritten: it holds work, and a command whose
// job is to create a starter file is the wrong place to destroy one.
func Init(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "a mod folder is required")
	}

	clean := filepath.Clean(dir)

	if _, _, ok := Find(clean); ok {
		return "", appErrors.New(appErrors.ErrCodeInvalidMod,
			"%s already has a manifest", clean)
	}

	if err := os.MkdirAll(clean, 0o750); err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeConfigIO,
			"create mod folder %s", clean)
	}

	path := filepath.Join(clean, Names[0])
	if err := os.WriteFile(path, []byte(Template), 0o600); err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeConfigIO,
			"write manifest %s", path)
	}

	zlog.Info().Str("op", "manifest-init").Str("path", path).Msg("manifest created")

	return path, nil
}
