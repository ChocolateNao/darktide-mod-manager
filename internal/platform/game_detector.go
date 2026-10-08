package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Detector finds Darktide installations and validates user-supplied paths.
type Detector struct {
	fs       FS
	getenv   Env
	now      func() time.Time
	platform domain.Platform
	home     string
	extras   []string
}

// NewDetector builds a detector. home may be empty, in which case it is taken
// from the environment. extras are user-configured paths tried before the
// built-in candidates.
func NewDetector(p domain.Platform, home string, getenv Env, extras []string) *Detector {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	if home == "" {
		if resolved, err := HomeDir(p, getenv); err == nil {
			home = resolved
		}
	}

	return &Detector{
		platform: p,
		home:     domain.CleanPath(home),
		getenv:   getenv,
		extras:   extras,
		fs:       RealFS(),
		now:      time.Now,
	}
}

// NewDetectorWithFS builds a detector with an injected filesystem, which lets
// tests simulate installations on any host.
func NewDetectorWithFS(
	p domain.Platform,
	home string,
	getenv Env,
	extras []string,
	fsys FS,
) *Detector {
	d := NewDetector(p, home, getenv, extras)
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
}

// Platform reports the platform being searched.
func (d *Detector) Platform() domain.Platform { return d.platform }

// FindInstallations probes every candidate and returns the ones that look like a
// Darktide installation, de-duplicated by path and ordered with the most likely
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

	for _, c := range ExpandCandidates(GameCandidates(d.platform, d.home, d.getenv, d.extras)) {
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
			"%q does not look like a Darktide installation: expected %s or data/mods inside it",
			path, domain.BinaryName)
	}

	install := &domain.GameInstallation{
		Path:       clean,
		Source:     source,
		Version:    d.detectVersion(clean),
		ModsPath:   d.resolveModsPath(clean),
		Valid:      true,
		DetectedAt: d.now(),
		Platform:   d.platform,
	}

	return install, nil
}

// gameMarkers are the files that identify a Darktide installation. The executable
// is authoritative on Windows; a data folder is enough on the Unix platforms,
// where the game is usually run through a translation layer that renames it.
func (d *Detector) looksLikeGame(dir string) bool {
	if ExistsNoFollow(d.fs, filepath.Join(dir, domain.UnixBinaryName)) {
		return true
	}

	if IsDir(d.fs, filepath.Join(dir, "data")) {
		return true
	}

	return IsDir(d.fs, filepath.Join(dir, domain.ModsDirName))
}

// modsPathCandidates lists the relative locations of the mods folder, most
// specific first. Darktide ships with a mods folder in the game root; older
// Proton prefixes sometimes keep it under data.
var modsPathCandidates = [][]string{
	{"data", domain.ModsDirName},
	{domain.ModsDirName},
	{"b3data", "temp", domain.ModsDirName},
}

// resolveModsPath returns an existing mods folder inside the installation, or the
// preferred default when none exists yet.
func (d *Detector) resolveModsPath(gameDir string) string {
	for _, rel := range modsPathCandidates {
		p := domain.JoinPath(append([]string{gameDir}, rel...)...)
		if IsDir(d.fs, p) {
			return p
		}
	}

	return domain.JoinPath(gameDir, domain.ModsDirName)
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

// versionMarkers are files that reveal the installed game build.
var versionMarkers = []string{"Darktide.exe", "Darktide", "version.txt"}

// detectVersion reads a build version out of the installation, returning "" when
// none is available. It never fails detection: a missing marker just means the
// version is unknown.
func (d *Detector) detectVersion(gameDir string) string {
	for _, marker := range versionMarkers {
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
