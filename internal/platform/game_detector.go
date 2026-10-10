package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/games/darktide"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Detector finds installations of a single registered game and validates
// user-supplied paths. Every candidate and marker is read from the game
// definition, so adding a game is registering its definition — no detector
// changes.
type Detector struct {
	fs       FS
	game     domain.GameDefinition
	getenv   Env
	now      func() time.Time
	platform domain.Platform
	home     string
	extras   []string
}

// NewDetector builds a detector for game. home may be empty, in which case it
// is taken from the environment. extras are user-configured paths tried before
// the built-in candidates. A nil game falls back to the built-in Darktide
// definition, keeping single-game call sites working.
func NewDetector(
	game domain.GameDefinition,
	p domain.Platform,
	home string,
	getenv Env,
	extras []string,
) *Detector {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	if home == "" {
		if resolved, err := HomeDir(p, getenv); err == nil {
			home = resolved
		}
	}

	if game == nil {
		game = darktide.New()
	}

	return &Detector{
		platform: p,
		home:     domain.CleanPath(home),
		getenv:   getenv,
		extras:   extras,
		fs:       RealFS(),
		now:      time.Now,
		game:     game,
	}
}

// NewDetectorWithFS builds a detector with an injected filesystem, which lets
// tests simulate installations on any host.
func NewDetectorWithFS(
	game domain.GameDefinition,
	p domain.Platform,
	home string,
	getenv Env,
	extras []string,
	fsys FS,
) *Detector {
	d := NewDetector(game, p, home, getenv, extras)
	if fsys != nil {
		d.fs = fsys
	}

	return d
}

// init fills in the dependencies a zero-value Detector is missing, so a Detector
// built by hand inside this package degrades to real filesystem access and the
// real clock instead of panicking.
func (d *Detector) init() {
	if d.fs == nil {
		d.fs = RealFS()
	}

	if d.getenv == nil {
		d.getenv = func(string) string { return "" }
	}

	if d.now == nil {
		d.now = time.Now
	}

	if d.game == nil {
		d.game = darktide.New()
	}
}

// Platform reports the platform being searched.
func (d *Detector) Platform() domain.Platform { return d.platform }

// FindInstallations probes every candidate and returns the ones that look like
// the configured game, de-duplicated by path and ordered with the most likely
// source first. A cancelled context stops the search and yields no results.
//
// Individual probe failures are not fatal: half the candidates will not exist.
func (d *Detector) FindInstallations(ctx context.Context) ([]domain.GameInstallation, error) {
	d.init()

	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return nil, errors.Wrap(err, errors.ErrCodeOperationCanceled, "game detection cancelled")
	}

	var found []domain.GameInstallation

	seen := map[string]bool{}

	candidates := GameCandidates(d.game, d.platform, d.home, d.getenv, d.extras)
	for _, c := range ExpandCandidates(candidates) {
		if err := ctx.Err(); err != nil {
			return nil, errors.Wrap(
				err,
				errors.ErrCodeOperationCanceled,
				"game detection cancelled",
			)
		}

		if seen[strings.ToLower(c.Path)] {
			continue
		}

		install, err := d.validate(c.Path, c.Source)
		if err != nil {
			continue
		}

		seen[strings.ToLower(c.Path)] = true

		found = append(found, *install)
	}

	return found, nil
}

// ValidateInstallation checks a user-supplied game path.
func (d *Detector) ValidateInstallation(path string) (*domain.GameInstallation, error) {
	d.init()
	return d.validate(path, domain.GameSourceManual)
}

// ValidateInstallationWithSource checks a user-supplied game path and records
// which launcher the user says owns it.
func (d *Detector) ValidateInstallationWithSource(
	path string,
	source domain.GameSource,
) (*domain.GameInstallation, error) {
	d.init()
	return d.validate(path, source)
}

func (d *Detector) validate(
	path string,
	source domain.GameSource,
) (*domain.GameInstallation, error) {
	clean := domain.CleanPath(path)
	if clean == "" {
		return nil, errors.New(errors.ErrCodeGameInvalid, "game path is empty")
	}

	if !looksAbsolute(clean) {
		return nil, errors.New(errors.ErrCodeGameInvalid,
			"game path %q must be absolute", path)
	}

	if !IsDir(d.fs, clean) {
		return nil, errors.Wrap(os.ErrNotExist, errors.ErrCodeGameNotFound,
			"game path %q does not exist or is not a directory", path)
	}

	if !d.looksLikeGame(clean) {
		return nil, errors.New(errors.ErrCodeGameInvalid,
			"%q does not look like a %s installation: expected %s or data/mods inside it",
			path, d.game.DisplayName(), d.primaryBinary())
	}

	install := &domain.GameInstallation{
		Path:       clean,
		Source:     source,
		Version:    d.detectVersion(clean),
		ModsPath:   d.resolveModsPath(clean),
		Valid:      true,
		DetectedAt: d.now(),
		Platform:   d.platform,
		GameID:     d.game.ID(),
	}

	return install, nil
}

// primaryBinary names a file that, present in the game root, proves the folder
// is the game. It backs the validation error message; a game without binaries
// falls back to the mods folder name.
func (d *Detector) primaryBinary() string {
	if names := d.game.BinaryNames(); len(names) > 0 {
		return names[0]
	}

	return d.game.ModsDirName()
}

// looksLikeGame reports whether the folder is the configured game: any declared
// binary present, a data folder (the Unix platforms usually run the game
// through a translation layer that renames it), or the mods folder.
func (d *Detector) looksLikeGame(dir string) bool {
	for _, name := range d.game.BinaryNames() {
		if ExistsNoFollow(d.fs, filepath.Join(dir, name)) {
			return true
		}
	}

	if IsDir(d.fs, filepath.Join(dir, "data")) {
		return true
	}

	return IsDir(d.fs, filepath.Join(dir, d.game.ModsDirName()))
}

// resolveModsPath returns an existing mods folder inside the installation, or
// the preferred default when none exists yet. Candidates come from the game
// definition, most specific first.
func (d *Detector) resolveModsPath(gameDir string) string {
	for _, rel := range d.game.ModsPathCandidates() {
		p := domain.JoinPath(append([]string{gameDir}, rel...)...)
		if IsDir(d.fs, p) {
			return p
		}
	}

	return domain.JoinPath(gameDir, d.game.ModsDirName())
}

// GetModsPath returns the mods folder for an installation, applying an override
// when one is configured. An override must name an existing directory: silently
// deploying into a mistyped folder would scatter mod files across the disk.
func (d *Detector) GetModsPath(install *domain.GameInstallation, override string) (string, error) {
	d.init()

	if install == nil {
		return "", errors.New(errors.ErrCodeGameInvalid, "no game installation given")
	}

	if trimmed := strings.TrimSpace(override); trimmed != "" {
		clean := domain.CleanPath(trimmed)
		if !looksAbsolute(clean) {
			return "", errors.New(errors.ErrCodeGameInvalid,
				"mods folder override %q must be absolute", override)
		}

		if !IsDir(d.fs, clean) {
			return "", errors.New(errors.ErrCodeModsPathNotDir,
				"mods folder override %q does not exist or is not a directory", override)
		}

		return clean, nil
	}

	if install.ModsPath == "" {
		return "", errors.New(
			errors.ErrCodeGameInvalid,
			"installation %s has no resolved mods path",
			install.Path,
		)
	}

	if !IsDir(d.fs, install.ModsPath) {
		return "", errors.New(
			errors.ErrCodeModsPathNotDir,
			"mods folder %s does not exist; create it or set a different override",
			install.ModsPath,
		)
	}

	return install.ModsPath, nil
}

// detectVersion reads a build version out of the installation, returning "" when
// none is available. It never fails detection: a missing marker just means the
// version is unknown. The markers come from the game definition.
func (d *Detector) detectVersion(gameDir string) string {
	for _, marker := range d.game.VersionMarkers() {
		p := filepath.Join(gameDir, marker)

		data, err := ReadFileLimit(p, 64*1024)
		if err != nil {
			continue
		}

		if v := extractVersion(string(data)); v != "" {
			return v
		}
	}

	return ""
}

// extractVersion pulls the first dotted numeric run out of a blob of text, which
// covers both a plain version file and a version string embedded in a log or
// launcher config.
func extractVersion(blob string) string {
	const maxScan = 64 * 1024
	if len(blob) > maxScan {
		blob = blob[:maxScan]
	}

	fields := strings.FieldsFunc(blob, func(r rune) bool {
		switch r {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '.':
			return false
		default:
			return true
		}
	})
	for _, f := range fields {
		if !isVersionLike(f) {
			continue
		}

		return strings.Trim(f, ".")
	}

	return ""
}

// isVersionLike requires at least one dot and at least one digit, so a stray
// number such as a file offset is not mistaken for a version.
func isVersionLike(s string) bool {
	if strings.Count(s, ".") == 0 {
		return false
	}

	digits := 0

	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
		default:
			return false
		}
	}

	return digits > 0
}
