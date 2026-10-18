package commands

import (
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
)

// ConfigCommands is the settings screen as a frontend sees it.
//
// A GUI with no way to read or write the configuration has to reach around
// itself for the sake of one settings page. Source settings are reachable from
// here through the same dotted keys the rest of the manager uses, so no
// source-specific method is needed.
type ConfigCommands struct {
	// Embedded so the lifecycle methods reach a set directly: a caller holding
	// one set must be able to shut it down without knowing the aggregate.
	*base
}

// NewConfigCommands builds the configuration command set.
func NewConfigCommands(deps Deps) *ConfigCommands {
	return &ConfigCommands{base: newBase(deps)}
}

func newConfigCommands(b *base) *ConfigCommands {
	return &ConfigCommands{base: b}
}

// GetConfig returns the whole configuration, as a copy, so a frontend editing
// the returned object cannot change what the manager is using until it calls
// UpdateConfig.
func (c *ConfigCommands) GetConfig() (*api.Config, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return nil, err
	}
	defer cancel()

	cfg, err := client.GetConfig(ctx)
	if err != nil {
		return nil, c.fail(err, "read the configuration")
	}

	return cfg, nil
}

// UpdateConfig writes a whole configuration, filling in defaults and saving only
// when something actually changed.
func (c *ConfigCommands) UpdateConfig(cfg *api.Config) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	if err := client.UpdateConfig(ctx, cfg); err != nil {
		return c.fail(err, "write the configuration")
	}

	return nil
}

// GetConfigValue reads one setting by its dotted key, for example
// "cache.max_size_gb" or "sources.local.enabled". Reading a credential through
// it is refused rather than answered: a secret a settings screen can read back
// is a secret a bug report can carry away, and the setting is still writable.
func (c *ConfigCommands) GetConfigValue(key string) (string, error) {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return "", err
	}
	defer cancel()

	value, err := client.GetConfigValue(ctx, strings.TrimSpace(key))
	if err != nil {
		return "", c.fail(err, "read setting %q", key)
	}

	return value, nil
}

// SetConfigValue writes one setting by its dotted key. The value is interpreted
// according to the setting, so a boolean rejects anything that is not a boolean
// rather than silently storing a string the manager will later fail to read.
func (c *ConfigCommands) SetConfigValue(key, value string) error {
	client, ctx, cancel, err := c.ready()
	if err != nil {
		return err
	}
	defer cancel()

	trimmed := strings.TrimSpace(key)
	if err := client.SetConfigValue(ctx, trimmed, value); err != nil {
		return c.fail(err, "write setting %q", key)
	}

	return nil
}

// ConfigKeys lists every settable key, including the sources.* patterns.
func (c *ConfigCommands) ConfigKeys() ([]string, error) {
	if _, _, _, err := c.ready(); err != nil {
		return nil, err
	}

	keys := api.ConfigKeys()
	if keys == nil {
		return []string{}, nil
	}

	return keys, nil
}

// ConfigPath is the file the settings are stored in.
func (c *ConfigCommands) ConfigPath() (string, error) {
	client, _, _, err := c.ready()
	if err != nil {
		return "", err
	}

	path, err := client.ConfigPath()
	if err != nil {
		return "", c.fail(err, "read the configuration path")
	}

	return path, nil
}
