package services

import (
	"sync"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

type CloudRegistry struct {
	providers map[string]domain.CloudProvider
	mu        sync.RWMutex
}

func NewCloudRegistry() *CloudRegistry {
	return &CloudRegistry{
		providers: make(map[string]domain.CloudProvider),
	}
}

func (r *CloudRegistry) Register(p domain.CloudProvider) error {
	if r == nil {
		return appErrors.ErrInvalidArgument
	}

	if p == nil {
		return appErrors.New(appErrors.ErrCodeInvalidArgument, "nil provider")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.providers[p.ID()] = p

	return nil
}

func (r *CloudRegistry) Get(id string) (domain.CloudProvider, error) {
	if r == nil {
		return nil, appErrors.ErrInvalidArgument
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.providers[id]
	if !ok {
		return nil, appErrors.Wrap(domain.ErrCloudProviderNotFound,
			appErrors.ErrCodeCloudProviderNotFound, "provider %q is not registered", id)
	}

	return p, nil
}

func (r *CloudRegistry) List() []domain.CloudProvider {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]domain.CloudProvider, 0, len(r.providers))
	for _, p := range r.providers {
		result = append(result, p)
	}

	return result
}
