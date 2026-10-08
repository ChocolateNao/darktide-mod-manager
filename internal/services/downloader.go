package services

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// RegistryDownloader is the cache manager's downloader when the mod being
// fetched comes from a registered source.
//
// The cache manager holds one downloader, but a mod's bytes come from whichever
// source owns it, and every bundled source both lists and downloads its own mods.
// Without this adapter the cache can only ever be a reader, and every install
// would stop at "no downloader is configured" — a failure that reads as a missing
// plugin rather than a missing wire.
type RegistryDownloader struct {
	registry *SourceRegistry
}

// NewRegistryDownloader returns a downloader that resolves each mod's source
// through the registry.
func NewRegistryDownloader(registry *SourceRegistry) *RegistryDownloader {
	return &RegistryDownloader{registry: registry}
}

// sourceFor resolves the source that owns a mod.
//
// The registry builds sources without a context because that work is local, so
// the cancellation check is made here rather than dropped.
func (d *RegistryDownloader) sourceFor(
	ctx context.Context,
	mod *domain.Mod,
) (domain.ModSource, error) {
	if d == nil || d.registry == nil {
		return nil, appErrors.New(appErrors.ErrCodeSourceUnavailable,
			"no source registry is available to fetch %s", modLabel(mod))
	}

	if mod == nil {
		return nil, appErrors.New(appErrors.ErrCodeInvalidArgument, "cannot fetch a nil mod")
	}

	if ctx != nil && ctx.Err() != nil {
		return nil, canceledError(ctx.Err())
	}

	return d.registry.Get(mod.SourceID)
}

// Download fetches a mod through the source that owns it.
func (d *RegistryDownloader) Download(
	ctx context.Context,
	mod *domain.Mod,
	destPath string,
) (string, error) {
	source, err := d.sourceFor(ctx, mod)
	if err != nil {
		return "", err
	}

	downloader, ok := source.(Downloader)
	if !ok {
		// An unknown source and a source that cannot download are different
		// problems, and only the latter is the user's configuration to fix, so
		// the message has to name the source it is about.
		return "", appErrors.New(appErrors.ErrCodeSourceUnavailable,
			"source %q cannot download files itself", source.ID())
	}

	return downloader.Download(ctx, mod, destPath)
}

// Verify checks a downloaded file through the source that owns the mod.
//
// A source that cannot download claims no digest, so there is nothing to verify
// against and reporting a failure would reject every mod it lists. Returning nil
// is therefore the only honest answer for one.
func (d *RegistryDownloader) Verify(ctx context.Context, mod *domain.Mod, path string) error {
	if mod == nil {
		return nil
	}

	source, err := d.sourceFor(ctx, mod)
	if err != nil {
		return err
	}

	downloader, ok := source.(Downloader)
	if !ok {
		return nil
	}

	return downloader.Verify(ctx, mod, path)
}

var _ Downloader = (*RegistryDownloader)(nil)
