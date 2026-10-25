// Package manifest owns the mod manifest: the mod.toml a mod folder may carry,
// the names it is allowed to have, and how it is written.
//
// Every part of the manager that touches the format goes through here. A format
// with three owners has three answers to "what does a manifest look like", and
// only one of them is true.
package manifest

import (
	"os"
	"path/filepath"
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
	for _, candidate := range Names {
		if strings.EqualFold(name, candidate) {
			return true
		}
	}

	return false
}

// Find returns the manifest file a mod folder is actually read from, and
// whether the folder has one at all. A *directory* named mod.toml does not
// count: that is a folder the author made, not a manifest.
func Find(dir string) (name, path string, ok bool) {
	for _, candidate := range Names {
		full := filepath.Join(dir, candidate)

		info, err := os.Stat(full)
		if err == nil && !info.IsDir() {
			return candidate, full, true
		}
	}

	return "", "", false
}

// Has reports whether dir directly contains a recognised manifest file.
func Has(dir string) bool {
	_, _, ok := Find(dir)

	return ok
}

// Read parses the manifest of a mod folder, the first recognised file winning.
//
// Every field is optional: a manifest that only sets an id is a valid mod, and a
// folder with no manifest at all is discovered under its folder name. That
// leniency is the point — mod authors are not going to write perfect manifests,
// and a reader that rejected incomplete ones would mostly report "nothing found".
func Read(dir string) (*Manifest, error) {
	_, path, ok := Find(dir)
	if !ok {
		return &Manifest{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read manifest %q", path)
	}

	var m Manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidMod,
			"parse manifest %q: %v", path, err)
	}

	// Hook languages are validated here, at parse time: a manifest naming an unknown
	// script language is a broken mod, and reporting it now keeps discovery from
	// half-loading it.
	if _, err := m.DomainHooks(); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidMod,
			"parse manifest %q: %v", path, err)
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
