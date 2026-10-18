package api

import (
	"context"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/hooks/host"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/rs/zerolog"
)

// The providers handed to the hook sandbox. They are the only bridge between a
// script and the manager's state: the host checks permissions first, then calls
// through one of these.

// hookConfigAccess lets a script read and write the configuration through the
// same dotted-key API the CLI and the GUI use.
type hookConfigAccess struct {
	store services.ConfigStore
}

// Get reads one dotted key from the stored configuration.
func (a hookConfigAccess) Get(key string) (string, error) {
	cfg, err := a.load()
	if err != nil {
		return "", err
	}

	return ConfigValue(cfg, key)
}

// Set writes one dotted key and saves the configuration. The hooks.permissions
// namespace is refused here as well as in the host: the permission model must not be
// editable by the code it constrains, even if a future host forgets to check.
func (a hookConfigAccess) Set(key, value string) error {
	normalized := normalizeConfigKey(key)
	if normalized == "hooks.permissions" ||
		strings.HasPrefix(normalized, "hooks.permissions.") {
		return appErrors.New(appErrors.ErrCodeHookDenied,
			"hook scripts may not modify hooks.permissions.*")
	}

	cfg, err := a.load()
	if err != nil {
		return err
	}

	if err := SetConfigValue(cfg, key, value); err != nil {
		return err
	}

	if err := a.store.Save(cfg); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeConfigIO,
			"could not save the configuration: %v", err)
	}

	return nil
}

// Values returns every readable dotted key. Secrets and per-source API keys
// are skipped: a script that can read the configuration can read a credential
// out of it, and there is no reason to hand one over.
func (a hookConfigAccess) Values() (map[string]string, error) {
	cfg, err := a.load()
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(configFields))

	for _, field := range configFields {
		if secretConfigKeys[field.key] {
			continue
		}

		if strings.HasPrefix(field.key, "sources.") {
			continue
		}

		out[field.key] = field.get(cfg)
	}

	return out, nil
}

func (a hookConfigAccess) load() (*domain.Config, error) {
	if a.store == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"hook config access needs a configuration store")
	}

	cfg, err := a.store.Load()
	if err != nil {
		return nil, err
	}

	if cfg == nil {
		return nil, appErrors.New(appErrors.ErrCodeNotFound,
			"no configuration is loaded")
	}

	return cfg, nil
}

// hookStorageAccess lets a script inspect the mod storage: what is stored, and
// where one mod version's folder is.
type hookStorageAccess struct {
	index       hookModIndex
	storageRoot string
}

// hookModIndex is the slice of the mod repository the sandbox needs.
type hookModIndex interface {
	List(ctx context.Context) ([]domain.Mod, error)
}

// List returns one entry per stored mod version, with the resolved path.
func (a hookStorageAccess) List(ctx context.Context) ([]host.StorageEntry, error) {
	if a.index == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
			"hook storage access needs a mod index")
	}

	mods, err := a.index.List(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]host.StorageEntry, 0, len(mods))
	for _, m := range mods {
		out = append(out, host.StorageEntry{
			GameID:  m.GameID,
			ModID:   m.ID,
			Version: m.Version,
			Path: domain.ModStoragePath(
				a.storageRoot, m.GameID, m.ID, m.Version),
		})
	}

	return out, nil
}

// Path resolves one stored mod version's folder.
func (a hookStorageAccess) Path(
	_ context.Context, gameID, modID, version string,
) (string, error) {
	if strings.TrimSpace(modID) == "" {
		return "", appErrors.New(appErrors.ErrCodeInvalidArgument,
			"storage path needs a mod id")
	}

	return domain.ModStoragePath(a.storageRoot, gameID, modID, version), nil
}

// hookLogFunc forwards a script's log line to the process logger.
func hookLogFunc(logger *zerolog.Logger) host.LogFunc {
	if logger == nil {
		return nil
	}

	return func(level, msg string) {
		event := logger.Info()

		switch strings.ToLower(strings.TrimSpace(level)) {
		case "warn", "warning":
			event = logger.Warn()
		case "error":
			event = logger.Error()
		case "debug":
			event = logger.Debug()
		}

		event.Str("op", "hook.script").Msg(msg)
	}
}
