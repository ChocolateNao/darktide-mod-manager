package platform

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// Candidate is a place a game installation might live.
type Candidate struct {
	Path   string
	Source domain.GameSource
	Notes  string
	Glob   bool
}

// windowsProgramFiles returns the installed-program base directories, honouring
// the 32/64-bit split that Steam and Epic use.
func windowsProgramFiles(getenv Env) []string {
	var roots []string

	for _, key := range []string{"ProgramFiles(x86)", "ProgramFiles", "ProgramW6432"} {
		if v := envValue(getenv, key); v != "" {
			roots = append(roots, v)
		}
	}

	return roots
}

// steamRoots lists the Steam library roots for a platform. Registry lookups are
// not possible from pure Go without a Windows-only dependency, so the well-known
// environment variables and install locations are used instead.
func steamRoots(p domain.Platform, home string, getenv Env) []string {
	switch {
	case p.IsWindows():
		var roots []string

		for _, key := range []string{"SteamPath", "SteamInstallPath"} {
			if v := envValue(getenv, key); v != "" {
				roots = append(roots, v)
			}
		}

		for _, base := range windowsProgramFiles(getenv) {
			roots = append(roots, domain.JoinPath(base, "Steam"))
		}

		return roots
	case p.IsDarwin():
		return []string{
			domain.JoinPath(home, "Library", "Application Support", "Steam"),
			domain.JoinPath(home, "Applications", "Steam.app", "Contents", "Resources"),
		}
	default:
		return []string{
			domain.JoinPath(home, ".steam", "steam"),
			domain.JoinPath(home, ".steam", "root"),
			domain.JoinPath(home, ".local", "share", "Steam"),
			// Steam installed as a Flatpak keeps its library inside the sandbox.
			domain.JoinPath(home, ".var", "app", "com.valvesoftware.Steam", "data", "Steam"),
		}
	}
}

// GameCandidates enumerates every location worth probing for an installation
// of game on a platform, ordered from most likely to least. extras are
// user-supplied paths, which are probed first; each must be a single path.
//
// This is pure path derivation with no filesystem access, so the candidate list
// for any platform can be asserted from a test on any host.
func GameCandidates(
	game domain.GameDefinition,
	p domain.Platform,
	home string,
	getenv Env,
	extras []string,
) []Candidate {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	var out []Candidate

	for _, extra := range extras {
		if e := strings.TrimSpace(extra); e != "" {
			out = append(out, Candidate{
				Path:   domain.CleanPath(e),
				Source: domain.GameSourceManual,
				Notes:  "configured manually",
			})
		}
	}

	if game == nil {
		return out
	}

	// The folder name launchers install under, from the game definition. A
	// game that declares no install directory name only gets the manual paths.
	var gameDir string
	if names := game.InstallDirNames(); len(names) > 0 {
		gameDir = names[0]
	}

	if gameDir == "" {
		return out
	}

	// steamRelativeInstall is the location of the game inside a Steam library.
	steamRelativeInstall := []string{"steamapps", "common", gameDir}

	// bottleRelativeInstall is the location of the game inside a Windows
	// bottle created by CrossOver, Whisky, Heroic or Bottles.
	bottleRelativeInstall := []string{
		"drive_c", "Program Files (x86)", "Steam", "steamapps", "common", gameDir,
	}

	// protonRelativeInstall is the location of the game inside the Proton
	// prefix Steam maintains for the game's app id.
	protonRelativeInstall := []string{
		"steamapps", "compatdata", game.SteamAppID(), "pfx", "drive_c",
		"Program Files (x86)", "Steam", "steamapps", "common", gameDir,
	}
	hasSteam := game.SteamAppID() != ""

	// Steam: the native library layout, then the Proton prefix Steam maintains
	// for the app id, which is where the game actually lands on Linux.
	for _, root := range steamRoots(p, home, getenv) {
		if root == "" {
			continue
		}

		out = append(out, Candidate{
			Path:   domain.JoinPath(append([]string{root}, steamRelativeInstall...)...),
			Source: domain.GameSourceSteam,
			Notes:  "Steam library",
		})
		if !p.IsWindows() && hasSteam {
			out = append(out, Candidate{
				Path:   domain.JoinPath(append([]string{root}, protonRelativeInstall...)...),
				Source: domain.GameSourceProton,
				Notes:  "Steam Proton prefix",
			})
		}
	}

	switch {
	case p.IsWindows():
		for _, base := range windowsProgramFiles(getenv) {
			out = append(out,
				Candidate{
					Path:   domain.JoinPath(base, "Epic Games", gameDir),
					Source: domain.GameSourceEpic,
					Notes:  "Epic Games",
				},
				Candidate{
					Path:   domain.JoinPath(base, "ModsEasy", gameDir),
					Source: domain.GameSourceManual,
					Notes:  "ModsEasy",
				},
			)
		}
	case p.IsDarwin():
		out = append(out,
			Candidate{
				Path: domain.JoinPath(
					append(
						[]string{
							home,
							"Library",
							"Application Support",
							"CrossOver",
							"Bottles",
							"*",
						},
						bottleRelativeInstall...)...),
				Source: domain.GameSourceCrossOver,
				Glob:   true,
				Notes:  "CrossOver bottle",
			},
			Candidate{
				Path: domain.JoinPath(
					append(
						[]string{
							home,
							"Library",
							"Application Support",
							"com.whiskyapp.whisky",
							"bottles",
							"*",
						},
						bottleRelativeInstall...)...),
				Source: domain.GameSourceWhisky,
				Glob:   true,
				Notes:  "Whisky bottle",
			},
			Candidate{
				Path: domain.JoinPath(
					append([]string{home, "Parallels", gameDir}, bottleRelativeInstall...)...),
				Source: domain.GameSourceParallels,
				Notes:  "Parallels virtual machine",
			},
			Candidate{
				Path:   domain.JoinPath(home, "Library", "Application Support", "Epic", gameDir),
				Source: domain.GameSourceEpic,
				Notes:  "Epic Games",
			},
		)
	default:
		dataHome := envValue(getenv, "XDG_DATA_HOME")
		if !looksAbsolute(dataHome) {
			dataHome = domain.JoinPath(home, ".local", "share")
		}

		heroicPrefixes := envValue(getenv, "HEROIC_PREFIXES_PATH")
		if !looksAbsolute(heroicPrefixes) {
			heroicPrefixes = domain.JoinPath(dataHome, "heroic", "prefixes")
		}

		out = append(out,
			Candidate{
				Path:   domain.JoinPath(dataHome, "EpicGames", gameDir),
				Source: domain.GameSourceEpic,
				Notes:  "Epic Games",
			},
			Candidate{
				Path:   domain.JoinPath(home, ".EpicGames", gameDir),
				Source: domain.GameSourceEpic,
				Notes:  "Epic Games (legacy layout)",
			},
			Candidate{
				Path: domain.JoinPath(
					home,
					".var",
					"app",
					"com.heroicgameslauncher.hgl",
					"data",
					"EpicGames",
					gameDir,
				),
				Source: domain.GameSourceHeroic,
				Notes:  "Heroic Games Launcher (Flatpak)",
			},
			Candidate{
				Path: domain.JoinPath(
					append([]string{heroicPrefixes, "*"}, bottleRelativeInstall...)...),
				Source: domain.GameSourceHeroic,
				Glob:   true,
				Notes:  "Heroic Games Launcher prefix",
			},
			Candidate{
				Path: domain.JoinPath(
					append(
						[]string{home, ".lutris", "games", strings.ToLower(gameDir)},
						bottleRelativeInstall...)...),
				Source: domain.GameSourceLutris,
				Notes:  "Lutris game directory",
			},
			Candidate{
				Path: domain.JoinPath(
					append(
						[]string{
							home,
							".var",
							"app",
							"com.usebottles.bottles",
							"data",
							"bottles",
							"bottles",
							"*",
						},
						bottleRelativeInstall...)...),
				Source: domain.GameSourceBottles,
				Glob:   true,
				Notes:  "Bottles bottle",
			},
		)
	}

	return out
}

// ExpandCandidates resolves glob candidates against the filesystem and returns a
// deterministic, de-duplicated list with Glob cleared. A glob that matches
// nothing is dropped rather than reported as an error, because most launchers
// will not be installed at all.
func ExpandCandidates(candidates []Candidate) []Candidate {
	var out []Candidate

	seen := map[string]bool{}

	for _, c := range candidates {
		paths := []string{c.Path}
		if c.Glob {
			paths = expandGlob(c.Path)
		}

		for _, p := range paths {
			key := strings.ToLower(domain.CleanPath(p))
			if key == "" || seen[key] {
				continue
			}

			seen[key] = true

			out = append(
				out,
				Candidate{Path: domain.CleanPath(p), Source: c.Source, Notes: c.Notes},
			)
		}
	}

	return out
}

// expandGlob returns the sorted matches for a pattern. A malformed pattern, for
// instance one with an unbalanced bracket, yields no matches rather than an
// error: a broken candidate must not stop detection.
func expandGlob(pattern string) []string {
	if !strings.Contains(pattern, "*") {
		return nil
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}

	sort.Strings(matches)

	return matches
}
