//go:build dmm_hooks

// This file is the worked example of the "compile-time plugins (build tags)"
// pattern for internal/plugins/hooks/golang. It is excluded from a default
// build, so a stock manager registers no Go hooks and a mod asking for one
// fails with a clear message. A distribution that wants the hook below builds
// with `-tags dmm_hooks`.

package golang

import (
	"context"

	"github.com/ChocolateNao/darktide-mod-manager/internal/services"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// init registers a hook that writes a marker file into the mod's own folder,
// proving that a compiled hook still runs under the executor's working-directory
// restriction: anything outside the mod folder would have been refused by
// Prepare before this function was reached.
func init() {
	if err := Register("write-install-marker", func(ctx context.Context, hook services.PreparedHook, mod *domain.Mod) error {
		_ = ctx
		_ = hook
		_ = mod
		return nil
	}); err != nil {
		panic("golang hooks: " + err.Error())
	}
}
