package di

import (
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// apiOptions maps this package's Options onto the public API's options.
//
// It returns an error rather than skipping a bad field: a source id spelled
// wrongly would otherwise start a manager that quietly ignored half its
// configuration.
func (o Options) apiOptions() ([]api.Option, error) {
	opts := make([]api.Option, 0, len(o.SourceConfigs)+5)

	if path, ok := normalizePath(o.ConfigPath); ok {
		opts = append(opts, api.WithConfigPath(path))
	}
	// The game path is not trimmed-and-ignored: it is validated against the
	// filesystem, so a path of nothing but spaces must be reported.
	if path, ok := normalizePath(o.GamePath); ok {
		opts = append(opts, api.WithGamePath(path))
	}

	// The game id is trimmed like a path, but a blank means "whatever the
	// configuration says" rather than an error: there is no filesystem validation
	// to tell a user their spaces were wrong.
	if id := strings.TrimSpace(o.GameID); id != "" {
		opts = append(opts, api.WithGame(id))
	}

	if path, ok := normalizePath(o.CachePath); ok {
		opts = append(opts, api.WithCachePath(path))
	}

	if path, ok := normalizePath(o.StoragePath); ok {
		opts = append(opts, api.WithStoragePath(path))
	}

	// The logger goes to the wiring phase as well as to the client: the wiring runs
	// first and is where the only interesting start-up diagnostic is produced —
	// there is no client yet at that point to log it through.
	if o.Logger != nil {
		opts = append(opts, api.WithLogger(o.Logger))
	}

	// The logging overrides go in unconditionally: the service merges them over
	// the persisted block itself, and an always-present option keeps this
	// function from growing a "does anything here have an opinion?" branch.
	opts = append(opts, api.WithLoggingOptions(o.loggingOptions()))

	// Sorted, so two runs over the same map build their sources in the same order.
	// Nothing depends on it, but a start-up log listing the configured sources
	// should not reshuffle between two launches of the same configuration.
	ids := make([]string, 0, len(o.SourceConfigs))
	for id := range o.SourceConfigs {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	for _, id := range ids {
		block := o.SourceConfigs[id]

		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			return nil, appErrors.New(appErrors.ErrCodeInvalidArgument,
				"a source configuration has a blank source id")
		}

		opts = append(opts, api.WithSourceConfig(trimmed, block))
	}

	return opts, nil
}

// clientOptions supplies the options the client itself needs.
//
// They are separate from the bundle's because api.NewClientFromServices refuses
// anything that re-points the graph. A logger re-points nothing, so it belongs to
// the client as well as to the wiring — a caller whose logger never sees
// Client.Logf would find the public logging API throwing its output away.
func (o Options) clientOptions() []api.Option {
	opts := make([]api.Option, 0, 2)
	if o.EventHandler != nil {
		opts = append(opts, api.WithEventHandler(o.EventHandler))
	}

	if o.Logger != nil {
		opts = append(opts, api.WithLogger(o.Logger))
	}

	return opts
}

// Deps returns the service graph as the public API describes it.
//
// This is the supported way to build a second client over the same services. It
// is nil-receiver safe and reports a closed container rather than handing out
// pointers whose database has already been closed.
func (c *Container) Deps() (api.ClientDeps, error) {
	if c == nil || c.Closed() {
		return api.ClientDeps{}, errClosed()
	}

	return api.ClientDeps{
		Mods:    c.ModService,
		Sources: c.SourceRegistry,
		Game:    c.GameLocator,
		Cache:   c.CacheManager,
		Hooks:   c.HookExecutor,
		Config:  c.Store,
		Events:  c.EventBus,
		Clock:   c.clock,
	}, nil
}
