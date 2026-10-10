// Package sources wires the bundled mod-source plugins into a registry.
//
// Every source package registers its constructor from init, so importing this
// package is all that is needed for a source to be available. RegisterAll then
// turns that set of constructors into live sources, which is what the GUI, the
// CLI and the public API each call once at start-up.
//
// The split matters. Constructors are registered by init because Go has no other
// way for a package to announce itself; the sources themselves are built
// explicitly, because building one is fallible (a source needs credentials, a
// base URL, maybe a search endpoint) and a start-up that failed because one
// optional source was misconfigured would be worse than useless.
package sources

import (
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/github"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/httpx"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/local"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/sources/shared/nexus"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// The imports above are not decoration. Each source package registers its
// factory from init, so a blank import is the mechanism that makes the plugin
// visible at all. Dropping one silently removes a source from the manager.
var (
	_ = github.SourceID
	_ = local.SourceID
	_ = nexus.SourceID
)

// IDs lists the bundled source ids, in the order a user is most likely to meet
// them. It is derived from the registry rather than hard-coded, so a plugin
// added later does not need a second edit here.
func IDs() []string { return services.RegisteredFactoryIDs() }

// RegisterAll builds every registered source whose configuration block enables
// it and adds it to registry. The game is the active one: it is attached to
// the registry (so later lazy builds resolve its game-scoped factories first)
// and handed to every factory as the build context.
//
// The returned error names every source that failed to build, but the sources
// that did build are registered either way. A source with a missing API key, an
// unreachable base URL or a broken plugin must not take the other three down
// with it — the user can still manage their installed mods without, say, Nexus.
//
// A configuration block that is absent counts as enabled. A fresh install has
// no blocks at all, and a manager that started with no sources in it would look
// broken; this matches SourceRegistry.EnabledSources, which treats a missing
// block the same way.
func RegisterAll(
	registry *services.SourceRegistry,
	cfg map[string]domain.SourceConfig,
	game domain.GameDefinition,
) error {
	if registry == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "nil source registry")
	}

	registry.WithGame(game)

	blocks := normalizeBlocks(cfg)

	var failures []error
	// The id lists are sorted, so registration order does not depend on map
	// iteration or on link order.
	for _, id := range factoryIDs(game) {
		// The block is hoisted out of the Enabled check because the factory needs
		// it even when the block is absent.
		block, present := blocks[id]
		if present && !block.Enabled {
			continue
		}

		factory, ok := services.SourceFactoryFor(id)
		if game != nil {
			// Game-scoped registrations win: a source that exists for one game
			// describes that game's build of the id, even when a generic
			// factory for the same id is installed.
			if gameFactory, gameOK := services.SourceFactoryForGame(game.ID(), id); gameOK {
				factory, ok = gameFactory, true
			}
		}

		if !ok {
			// The id came from the registry a moment ago, so this cannot happen
			// unless another goroutine unregistered in between. Skipping is the
			// right answer: there is nothing to build and nothing to report.
			continue
		}

		source, err := factory(services.SourceBuildContext{Game: game, Config: block})
		if err != nil {
			failures = append(failures, appErrors.Wrap(err, appErrors.ErrCodeSourceUnavailable,
				"mod source %q could not be configured: %v", id, err))

			continue
		}

		if source == nil {
			failures = append(failures, appErrors.New(appErrors.ErrCodeSourceUnavailable,
				"mod source %q returned no source", id))

			continue
		}

		if err := registry.Register(source); err != nil {
			// Already registered is not a problem: something else got here first
			// (a second container, a test) and the outcome is what was wanted.
			if appErrors.IsAppError(err, appErrors.ErrCodeSourceExists) {
				continue
			}

			failures = append(failures, appErrors.Wrap(err, appErrors.ErrCodeSourceUnavailable,
				"mod source %q could not be registered: %v", id, err))
		}
	}

	return httpx.JoinErrors(failures)
}

// RegisterEnabled is RegisterAll without the configuration blocks: every
// registered source for the game is built. It exists for callers that have no
// per-source configuration to honour, such as a container built in a test.
func RegisterEnabled(registry *services.SourceRegistry, game domain.GameDefinition) error {
	return RegisterAll(registry, nil, game)
}

// factoryIDs lists every source id registerable for the game — generic
// factories plus the game's own — sorted and deduplicated.
func factoryIDs(game domain.GameDefinition) []string {
	ids := services.RegisteredFactoryIDs()

	if game != nil {
		ids = append(ids, services.RegisteredGameFactoryIDs(game.ID())...)
	}

	sort.Strings(ids)

	out := ids[:0]

	for i, id := range ids {
		if i > 0 && id == ids[i-1] {
			continue
		}

		out = append(out, id)
	}

	return out
}

// Enabled reports whether the configuration map enables a source. A missing
// block counts as enabled, for the same reason RegisterAll does it.
func Enabled(cfg map[string]domain.SourceConfig, id string) bool {
	block, ok := normalizeBlocks(cfg)[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return true
	}

	return block.Enabled
}

// normalizeBlocks re-keys a configuration map so every key is lower-cased and
// trimmed, matching how source ids are normalised everywhere else.
//
// It matters because the keys come out of a file the user is free to edit. A
// config naming "GitHub" rather than "github" describes the same source, and a
// lookup by the registry's own key would silently ignore it — the user sets a
// token in their config and watches it never arrive. Both RegisterAll and
// Enabled go through here so the two cannot disagree.
//
// Keys are visited in sorted order and the first spelling of a given id wins.
// A file naming the same source twice is a mistake, and pinning the outcome to
// sorted order is what keeps it from becoming a different answer on each start.
func normalizeBlocks(cfg map[string]domain.SourceConfig) map[string]domain.SourceConfig {
	out := make(map[string]domain.SourceConfig, len(cfg))

	keys := make([]string, 0, len(cfg))
	for key := range cfg {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized == "" {
			continue
		}

		if _, seen := out[normalized]; seen {
			continue
		}

		out[normalized] = cfg[key]
	}

	return out
}
