package logging

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/platform"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Five backups at 10 MB bounds the log set at 60 MB; older generations are
// pruned by age and deleted on rotation.
const (
	// LogFileName is the active log file inside the log directory.
	LogFileName = "dmm.log"
	// DefaultMaxSizeMB is the size that triggers a rotation.
	DefaultMaxSizeMB = 10
	// DefaultMaxBackups is the number of rotated files kept.
	DefaultMaxBackups = 5
	// DefaultMaxAgeDays is how long a rotated file is kept.
	DefaultMaxAgeDays = 30
	// DefaultCompress gzips rotated files.
	DefaultCompress = true
)

// EnvLogDir is the environment variable that redirects the log directory. It
// sits between --log-dir and config.json so a shell profile can move logs
// without editing the config.
const EnvLogDir = "DMM_LOG_DIR"

// newRotator builds the lumberjack writer for dir. LocalTime keeps rotated file
// names aligned with the wall clock the user sees in `dmm logs list`.
func newRotator(dir string, cfg Config) *lumberjack.Logger {
	return &lumberjack.Logger{
		Filename:   filepath.Join(dir, LogFileName),
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
		LocalTime:  true,
	}
}

// LogPath is the active log file inside dir.
func LogPath(dir string) string { return filepath.Join(dir, LogFileName) }

// ResolveDirectory applies the documented precedence:
//
//	--log-dir  >  DMM_LOG_DIR  >  logging.directory  >  platform log directory
//
// A relative override is rejected: a log path that depends on the working
// directory scatters the logs.
func ResolveDirectory(override, configured string) (string, error) {
	if override = strings.TrimSpace(override); override != "" {
		if !filepath.IsAbs(override) {
			return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
				"log directory must be an absolute path: %s", override)
		}

		return filepath.Clean(override), nil
	}

	if env := strings.TrimSpace(os.Getenv(EnvLogDir)); env != "" {
		if !filepath.IsAbs(env) {
			return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
				"%s must be an absolute path: %s", EnvLogDir, env)
		}

		return filepath.Clean(env), nil
	}

	if configured = strings.TrimSpace(configured); configured != "" {
		return filepath.Clean(configured), nil
	}

	dirs, err := platform.CurrentDirs()
	if err != nil {
		return "", err
	}

	return dirs.Logs, nil
}

// EnsureDirectory creates the log directory and reports whether the file sink
// is usable. It is separate from ResolveDirectory because callers that want to
// warn the user (rather than silently degrade) need the distinction.
func EnsureDirectory(dir string) error {
	if dir == "" {
		return nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigInvalid,
			"could not create log directory %s", dir)
	}

	return nil
}
