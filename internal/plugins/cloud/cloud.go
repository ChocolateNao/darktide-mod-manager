package cloud

import (
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/dropbox"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/gdrive"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/onedrive"
	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/s3"
	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/rs/zerolog"
)

// ProviderFactory builds a CloudProvider from a provider's settings block.
// The factory owns credential handling, so the wiring here never sees a token.
type ProviderFactory func(opts map[string]string) (domain.CloudProvider, error)

// factories holds the registered provider factories, keyed by provider id.
// They register themselves through init(), the same way the mod source plugins
// do, so adding a provider means adding one file and no wiring change.
var factories = map[string]ProviderFactory{}

// RegisterFactory registers a factory under a provider id. It is called from
// init() by each provider package.
func RegisterFactory(id string, factory ProviderFactory) {
	factories[id] = factory
}

// FactoryIDs lists the registered provider ids, sorted. It reports the
// providers this build knows about, whether or not the user configured them.
func FactoryIDs() []string {
	ids := make([]string, 0, len(factories))
	for id := range factories {
		ids = append(ids, id)
	}

	// The list is tiny and rendered in a picker, so allocation is not a concern.
	sortStrings(ids)

	return ids
}

// Build constructs a provider from its settings block. An unknown id reports
// the typed cloud provider error so a caller can match on it.
func Build(id string, opts map[string]string) (domain.CloudProvider, error) {
	factory, ok := factories[id]
	if !ok {
		return nil, domain.ErrCloudProviderNotFound
	}

	return factory(opts)
}

// RegisterConfigured builds every provider the configuration names and
// registers them on the registry. Providers whose config is missing or
// incomplete are skipped rather than failing the whole wiring: a share to a
// provider the user never set up is a dead share, and the picker should simply
// not offer it.
func RegisterConfigured(
	registry *services.CloudRegistry,
	cfg domain.CloudSharingConfig,
	logger zerolog.Logger,
) error {
	if registry == nil {
		return nil
	}

	for id, opts := range cfg.Providers {
		provider, err := Build(id, opts)
		if err != nil {
			logger.Warn().Str("provider", id).Err(err).Msg("cloud provider not built")
			continue
		}

		if err := registry.Register(provider); err != nil {
			logger.Warn().Str("provider", id).Err(err).Msg("cloud provider not registered")
			continue
		}
	}

	return nil
}

func sortStrings(items []string) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j] < items[j-1]; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

func init() {
	// Each provider registers itself the same way a mod source plugin does, so
	// adding one means adding one file and no wiring change here.
	RegisterFactory(s3.ProviderID, func(opts map[string]string) (domain.CloudProvider, error) {
		return s3.New(s3.Config{
			Endpoint:  opts["endpoint"],
			Region:    opts["region"],
			AccessKey: opts["access_key"],
			SecretKey: opts["secret_key"],
			Bucket:    opts["bucket"],
			UseSSL:    opts["use_ssl"] == "true",
		})
	})

	RegisterFactory(gdrive.ProviderID, func(opts map[string]string) (domain.CloudProvider, error) {
		return gdrive.New(gdrive.Config{
			ClientID:     opts["client_id"],
			ClientSecret: opts["client_secret"],
			FolderID:     opts["folder_id"],
		})
	})

	RegisterFactory(dropbox.ProviderID, func(opts map[string]string) (domain.CloudProvider, error) {
		return dropbox.New(dropbox.Config{
			ClientID:     opts["client_id"],
			ClientSecret: opts["client_secret"],
			Folder:       opts["folder"],
		})
	})

	RegisterFactory(
		onedrive.ProviderID,
		func(opts map[string]string) (domain.CloudProvider, error) {
			return onedrive.New(onedrive.Config{
				ClientID:     opts["client_id"],
				ClientSecret: opts["client_secret"],
				Folder:       opts["folder"],
			})
		},
	)
}
