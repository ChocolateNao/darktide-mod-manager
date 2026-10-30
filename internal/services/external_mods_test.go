package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testLoadOrder = "mod_load_order.txt"

func gameWithLoadOrder(t *testing.T) *harness {
	t.Helper()

	return newHarness(t, func(o *harnessOpts) {
		o.locator = o.locator.withLoadOrderFile(testLoadOrder)
	})
}

func writeLoadOrderFile(t *testing.T, h *harness, lines ...string) {
	t.Helper()

	path := filepath.Join(h.locator.modsPath, testLoadOrder)
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func addModFolder(t *testing.T, h *harness, name string) string {
	t.Helper()

	path := filepath.Join(h.locator.modsPath, name)
	require.NoError(t, os.MkdirAll(path, 0o755))

	return path
}

func TestListModsMarksUnknownFoldersExternal(t *testing.T) {
	h := gameWithLoadOrder(t)
	h.index.put(&domain.Mod{
		ID: "known", Name: "Known", GameID: "",
		ModFolderName: "known_mod", InstallMode: domain.InstallModeModsFolder, Enabled: true,
	})
	addModFolder(t, h, "known_mod")
	addModFolder(t, h, "external_mod")
	writeLoadOrderFile(t, h, "known_mod", "external_mod")

	mods, err := h.svc.ListMods(context.Background())
	require.NoError(t, err)

	require.Len(t, mods, 2)

	byID := map[string]domain.Mod{}
	for _, mod := range mods {
		byID[mod.ID] = mod
	}

	require.Contains(t, byID, "external:external_mod")
	assert.True(t, byID["external:external_mod"].External)
	assert.True(t, byID["external:external_mod"].Enabled)
	assert.Equal(t, "external_mod", byID["external:external_mod"].ModFolderName)
	assert.True(t, byID["known"].Enabled)
}

func TestListModsMarksUnlistedExternalFolderDisabled(t *testing.T) {
	h := gameWithLoadOrder(t)
	addModFolder(t, h, "external_mod")
	writeLoadOrderFile(t, h)

	mods, err := h.svc.ListMods(context.Background())
	require.NoError(t, err)
	require.Len(t, mods, 1)

	assert.True(t, mods[0].External)
	assert.False(t, mods[0].Enabled, "an external folder absent from the load order is disabled")
}

func TestListModsWithoutLoadOrderFileDoesNotInventExternalMods(t *testing.T) {
	h := newHarness(t)
	addModFolder(t, h, "external_mod")

	mods, err := h.svc.ListMods(context.Background())
	require.NoError(t, err)
	assert.Empty(t, mods)
}

func TestListModsReconcilesKnownModWithLoadOrder(t *testing.T) {
	h := gameWithLoadOrder(t)
	h.index.put(&domain.Mod{
		ID: "known", Name: "Known", GameID: "",
		ModFolderName: "known_mod", InstallMode: domain.InstallModeModsFolder, Enabled: true,
	})
	addModFolder(t, h, "known_mod")
	writeLoadOrderFile(t, h)

	mods, err := h.svc.ListMods(context.Background())
	require.NoError(t, err)
	require.Len(t, mods, 1)

	assert.False(t, mods[0].Enabled, "a known mod missing from the file is disabled")
	assert.False(t, h.installed(t, "known").Enabled, "the reconciliation is persisted")
}

func TestExternalModCannotBeEnabledOrDisabled(t *testing.T) {
	h := gameWithLoadOrder(t)
	addModFolder(t, h, "external_mod")

	for _, call := range []struct {
		name string
		run  func() error
	}{
		{"enable", func() error { return h.svc.EnableMod(context.Background(), "external:external_mod") }},
		{"disable", func() error { return h.svc.DisableMod(context.Background(), "external:external_mod") }},
	} {
		t.Run(call.name, func(t *testing.T) {
			err := call.run()
			require.Error(t, err)
			assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument))
		})
	}
}

func TestEnablingKnownModOnlyEditsLoadOrderFile(t *testing.T) {
	h := gameWithLoadOrder(t)
	h.withArchive(t, modFixture("known", "1.0.0"), "known_mod/file.txt")

	disabled := false
	_, err := h.svc.Install(context.Background(), "vortex", "known", InstallOptions{Enable: &disabled})
	require.NoError(t, err)

	deploys := len(h.deployer.deployCalls)

	require.NoError(t, h.svc.EnableMod(context.Background(), "known"))

	content, err := os.ReadFile(filepath.Join(h.locator.modsPath, testLoadOrder))
	require.NoError(t, err)
	assert.Contains(t, string(content), "known_mod")
	assert.Len(t, h.deployer.deployCalls, deploys, "enabling must not deploy files")
}

func TestDisablingKnownModKeepsDeployedFiles(t *testing.T) {
	h := gameWithLoadOrder(t)
	h.withArchive(t, modFixture("known", "1.0.0"), "known_mod/file.txt")

	_, err := h.svc.Install(context.Background(), "vortex", "known", InstallOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, h.deployer.deployCalls)

	require.NoError(t, h.svc.DisableMod(context.Background(), "known"))

	assert.Empty(t, h.deployer.undeployed, "disabling must not remove files from the game folder")
	assert.NotEmpty(t, h.deployer.deployed, "the deployed files stay in place")

	content, err := os.ReadFile(filepath.Join(h.locator.modsPath, testLoadOrder))
	require.NoError(t, err)
	assert.NotContains(t, string(content), "known_mod")
}

func TestReorderModsWritesExternalFolders(t *testing.T) {
	h := gameWithLoadOrder(t)
	addModFolder(t, h, "first")
	addModFolder(t, h, "second")
	writeLoadOrderFile(t, h, "first", "second")

	require.NoError(t, h.svc.ReorderMods(context.Background(),
		[]string{"external:second", "external:first"}))

	content, err := os.ReadFile(filepath.Join(h.locator.modsPath, testLoadOrder))
	require.NoError(t, err)
	assert.Equal(t, "second\nfirst\n", string(content))
}

func TestReorderModsSkipsDisabledExternalMod(t *testing.T) {
	h := gameWithLoadOrder(t)
	addModFolder(t, h, "listed")
	addModFolder(t, h, "disabled")
	writeLoadOrderFile(t, h, "listed")

	require.NoError(t, h.svc.ReorderMods(context.Background(),
		[]string{"external:disabled", "external:listed"}))

	content, err := os.ReadFile(filepath.Join(h.locator.modsPath, testLoadOrder))
	require.NoError(t, err)
	assert.Equal(t, "listed\n", string(content), "a disabled external mod stays out of the file")
}

func TestInstallingDisabledKnownModStillPlacesPayload(t *testing.T) {
	h := gameWithLoadOrder(t)
	h.withArchive(t, modFixture("known", "1.0.0"), "known_mod/file.txt")

	_, err := h.svc.Install(context.Background(), "vortex", "known",
		InstallOptions{Enable: boolPtr(false)})
	require.NoError(t, err)

	assert.NotEmpty(t, h.deployer.deployCalls,
		"a file-driven game needs the payload on disk even while the mod is disabled")
	assert.False(t, h.installed(t, "known").Enabled)

	content, err := os.ReadFile(filepath.Join(h.locator.modsPath, testLoadOrder))
	if err == nil {
		assert.NotContains(t, string(content), "known_mod", "a disabled install is not listed")
	}
}

func TestGameRootModStillTogglesOnLoadOrderGame(t *testing.T) {
	h := gameWithLoadOrder(t)
	h.withArchive(t, modFixture("root", "1.0.0"), "root_mod/file.txt")

	_, err := h.svc.Install(context.Background(), "vortex", "root", InstallOptions{
		Enable:     boolPtr(false),
		SourceData: map[string]any{"install_mode": "game_root"},
	})
	require.NoError(t, err)
	require.Empty(t, h.deployer.deployCalls)

	require.NoError(t, h.svc.EnableMod(context.Background(), "root"))
	assert.NotEmpty(t, h.deployer.deployCalls, "a game-root mod is deployed, not listed")

	require.NoError(t, h.svc.DisableMod(context.Background(), "root"))
	assert.NotEmpty(t, h.deployer.undeployed, "a game-root mod is undeployed on disable")
}

func TestLoadOrderReturnsExternalIDs(t *testing.T) {
	h := gameWithLoadOrder(t)
	addModFolder(t, h, "external_mod")
	writeLoadOrderFile(t, h, "external_mod")

	order, err := h.svc.LoadOrder(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"external:external_mod"}, order)
}

func TestUninstallExternalModDeletesFolderAndLoadOrderLine(t *testing.T) {
	h := gameWithLoadOrder(t)
	folder := addModFolder(t, h, "external_mod")
	writeLoadOrderFile(t, h, "external_mod")

	require.NoError(t, h.svc.Uninstall(context.Background(), "external:external_mod", UninstallOptions{}))

	_, err := os.Stat(folder)
	assert.True(t, os.IsNotExist(err), "the external folder must be gone")

	content, err := os.ReadFile(filepath.Join(h.locator.modsPath, testLoadOrder))
	require.NoError(t, err)
	assert.NotContains(t, string(content), "external_mod")
}

func TestUninstallExternalRefusesManagedFolder(t *testing.T) {
	h := gameWithLoadOrder(t)
	folder := addModFolder(t, h, "managed")
	h.index.put(&domain.Mod{
		ID: "managed", GameID: "", ModFolderName: "managed",
		InstallMode: domain.InstallModeModsFolder,
	})

	err := h.svc.Uninstall(context.Background(), "external:managed", UninstallOptions{})
	require.Error(t, err)
	assert.True(t, appErrors.IsAppError(err, appErrors.ErrCodeInvalidArgument))

	_, statErr := os.Stat(folder)
	assert.NoError(t, statErr, "a managed mod must not be deleted through the external path")
}
