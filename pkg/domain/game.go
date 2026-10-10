package domain

import (
	"strings"
	"time"
)

// GameSource identifies the store or launcher an installation came from.
type GameSource string

const (
	GameSourceSteam     GameSource = "steam"
	GameSourceEpic      GameSource = "epic"
	GameSourceMicrosoft GameSource = "microsoft"
	GameSourceHeroic    GameSource = "heroic"
	GameSourceLutris    GameSource = "lutris"
	GameSourceBottles   GameSource = "bottles"
	GameSourceFlatpak   GameSource = "flatpak"
	GameSourceProton    GameSource = "proton"
	GameSourceCrossOver GameSource = "crossover"
	GameSourceWhisky    GameSource = "whisky"
	GameSourceParallels GameSource = "parallels"
	GameSourceManual    GameSource = "manual"
)

// knownGameSources is every GameSource this build understands. Parsing and
// validation both work off this list so a new constant cannot be added without
// being recognised here too.
var knownGameSources = []GameSource{
	GameSourceSteam, GameSourceEpic, GameSourceMicrosoft, GameSourceHeroic,
	GameSourceLutris, GameSourceBottles, GameSourceFlatpak, GameSourceProton,
	GameSourceCrossOver, GameSourceWhisky, GameSourceParallels,
	GameSourceManual,
}

// ParseGameSource maps a configured launcher name onto a GameSource. Comparison
// is case-insensitive and surrounding space is ignored, because the value comes
// from a hand-editable JSON file.
//
// The boolean is false for anything unrecognised, including the empty string.
// "manual" is a real source but is not something a user selects to *filter*
// auto-detection by, so it is rejected here as well.
func ParseGameSource(s string) (GameSource, bool) {
	candidate := GameSource(strings.ToLower(strings.TrimSpace(s)))
	if candidate == "" {
		return "", false
	}

	for _, known := range knownGameSources {
		if known == GameSourceManual {
			continue
		}

		if known == candidate {
			return known, true
		}
	}

	return "", false
}

// GameInstallation is a candidate installation of a supported game found on
// this machine. GameID names the registered game it belongs to, so one scan
// can report installs of every supported game at once.
type GameInstallation struct {
	DetectedAt time.Time  `json:"detected_at"`
	GameID     string     `json:"game_id,omitempty"`
	Path       string     `json:"path"`
	Source     GameSource `json:"source"`
	AppID      string     `json:"app_id"`
	Version    string     `json:"version"`
	ModsPath   string     `json:"mods_path"`
	Platform   Platform   `json:"platform"`
	Notes      string     `json:"notes,omitempty"`
	Valid      bool       `json:"valid"`
}

// Known Darktide identifiers. These live here only until the platform layer is
// driven entirely by domain.GameDefinition; the registry copy in
// internal/games/darktide is the source of truth.
const (
	// SteamAppID is Darktide's Steam application ID. It also identifies the
	// Proton compatdata directory on Linux.
	SteamAppID = "1361210"
	// ModsDirName is the mods folder inside a Darktide installation.
	ModsDirName = "mods"
	// BinaryName is the primary executable, used to validate a path.
	BinaryName = "Darktide.exe"
	// UnixBinaryName is the launcher name on Proton prefixes.
	UnixBinaryName = "Darktide.exe"
)

// PathEqual compares two installation paths using case-insensitive semantics,
// which is correct on Windows and macOS and harmless on Linux (a case difference
// there means genuinely different paths, which callers can detect with
// StrictPathEqual when they need to).
func (g GameInstallation) PathEqual(other string) bool {
	return EqualFold(CleanPath(g.Path), CleanPath(other))
}
