package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// FromMod projects an installed mod onto the manifest format.
//
// Enabled, priority, install mode, the timestamps, the local path and the content
// hash describe *this machine's* copy, and writing them into a file that travels
// inside the mod folder would put one user's installation state into everyone
// else's mod. Everything that describes the mod itself comes across.
func FromMod(mod *domain.Mod) *Manifest {
	if mod == nil {
		return &Manifest{}
	}

	info := ModInfo{
		ID:            mod.ID,
		Name:          mod.Name,
		Version:       mod.Version,
		Author:        mod.Author,
		Description:   mod.Description,
		GameVersion:   mod.GameVersion,
		Dependencies:  TrimmedList(mod.Dependencies),
		ConflictsWith: TrimmedList(mod.ConflictsWith),
		Tags:          TrimmedList(mod.Tags),
		SourceURL:     mod.SourceURL,
		DownloadURL:   mod.DownloadURL,
		UpdateURL:     mod.UpdateURL,
		SourceData:    SourceDataStrings(mod.SourceData),
	}

	return &Manifest{Mod: info, Hooks: hooksFromMod(mod.Hooks)}
}

// hooksFromMod converts the stored hook scripts back into manifest entries.
//
// Hooks are the one part of the database that a manifest *is* rather than merely
// documents: dropping them would leave an installed mod that no longer runs its
// own lifecycle scripts.
func hooksFromMod(hooks domain.ModHooks) Hooks {
	var out Hooks

	for _, phase := range domain.HookPhases() {
		scripts := hooks.For(phase)
		if len(scripts) == 0 {
			continue
		}

		entries := make([]HookEntry, 0, len(scripts))
		for _, script := range scripts {
			entry := HookEntry{
				Type:       string(script.Language),
				Content:    script.Content,
				WorkingDir: script.WorkingDir,
				Env:        TrimmedList(script.Env),
				Timeout:    script.Timeout,
			}

			// A script with no language cannot be written and would not survive Read either.
			// Skipping it keeps Write from emitting a file that fails to parse.
			if strings.TrimSpace(entry.Type) == "" {
				continue
			}

			entries = append(entries, entry)
		}

		if len(entries) == 0 {
			continue
		}

		out.set(phase, entries)
	}

	return out
}

// set assigns one phase's entries, mirroring Manifest.phaseEntries in reverse.
// An unknown phase is ignored rather than panicking: domain.HookPhases is the
// authority on which phases exist, and a future phase added there without a
// manifest field must not crash a write.
func (h *Hooks) set(phase domain.HookPhase, entries []HookEntry) {
	switch phase {
	case domain.PhasePreInstall:
		h.PreInstall = entries
	case domain.PhasePostInstall:
		h.PostInstall = entries
	case domain.PhasePreEnable:
		h.PreEnable = entries
	case domain.PhasePostEnable:
		h.PostEnable = entries
	case domain.PhasePreDisable:
		h.PreDisable = entries
	case domain.PhasePostDisable:
		h.PostDisable = entries
	case domain.PhasePreUninstall:
		h.PreUninstall = entries
	case domain.PhasePostUninstall:
		h.PostUninstall = entries
	case domain.PhasePreUpdate:
		h.PreUpdate = entries
	case domain.PhasePostUpdate:
		h.PostUpdate = entries
	}
}

// SourceDataStrings flattens a mod's source-specific extras into the string map
// the manifest can hold.
//
// The database stores whatever a source plugin put there — numbers, booleans,
// nested objects, nulls — while TOML's `source_data` table is typed as strings
// because it is read by people and by tools that have no business re-implementing
// each source's schema. Scalars take their obvious spelling and anything
// structured becomes JSON; nil becomes an empty string rather than being dropped,
// because the key is part of what the source told us.
func SourceDataStrings(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = SourceDataValue(value)
	}

	return out
}

// SourceDataValue renders one source-data value as the string a manifest holds.
func SourceDataValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			// A value that cannot even be marshalled has no meaningful spelling, and
			// failing the whole write would lose the rest of the record.
			return fmt.Sprintf("%v", value)
		}

		return string(encoded)
	}
}

// Encode renders a manifest as TOML.
func Encode(m *Manifest) ([]byte, error) {
	if m == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "a manifest is required")
	}

	var buf bytes.Buffer

	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidMod, "encode manifest: %v", err)
	}

	return buf.Bytes(), nil
}

// Write renders a manifest into the mod folder at dir and returns the path it
// wrote.
//
// Unlike Init this overwrites an existing manifest, because its whole purpose is
// to refresh one from the database; refusing would make the command useless the
// second time. Which file it writes follows Read's priority order, so a mod that
// ships metadata.toml does not gain a second, competing mod.toml.
func Write(dir string, m *Manifest) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument, "a mod folder is required")
	}

	clean := filepath.Clean(dir)

	info, err := os.Stat(clean)
	switch {
	case err != nil:
		return "", appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "read mod folder %s", clean)
	case !info.IsDir():
		return "", appErrors.New(appErrors.ErrCodeInvalidMod, "%s is not a mod folder", clean)
	}

	data, err := Encode(m)
	if err != nil {
		return "", err
	}

	name := Names[0]
	if existing, _, ok := Find(clean); ok {
		name = existing
	}

	path := filepath.Join(clean, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", appErrors.Wrap(err, appErrors.ErrCodeConfigIO, "write manifest %s", path)
	}

	return path, nil
}
