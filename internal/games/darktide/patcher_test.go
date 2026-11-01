package darktide

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

func patcherTarGz(t *testing.T, name string, payload []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "dtkit-patch-0.1.8/" + name,
		Mode:     0o755,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
	}), "write tar header")
	_, err := tw.Write(payload)
	require.NoError(t, err, "write tar payload")
	require.NoError(t, tw.Close(), "close tar")
	require.NoError(t, gz.Close(), "close gzip")

	return buf.Bytes()
}

func patcherZip(t *testing.T, name string, payload []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	entry, err := zw.Create(name)
	require.NoError(t, err, "create zip entry")
	_, err = entry.Write(payload)
	require.NoError(t, err, "write zip payload")
	require.NoError(t, zw.Close(), "close zip")

	return buf.Bytes()
}

func TestPatcherAssetMatchesThePlatform(t *testing.T) {
	assets := []releaseAsset{
		{Name: "dtkit-patch-0.1.8-x86_64-pc-windows-msvc.zip", URL: "windows"},
		{Name: "dtkit-patch-0.1.8-x86_64-unknown-linux-musl.tar.gz", URL: "linux"},
		{Name: "dtkit-patch-0.1.8-aarch64-unknown-linux-musl.tar.gz", URL: "arm"},
		{Name: "dtkit-patch-0.1.8-source.tar.gz", URL: "source"},
	}

	tests := []struct {
		name     string
		platform domain.Platform
		arch     string
		want     string
		wantOK   bool
	}{
		{"linux amd64", domain.PlatformLinux, "amd64", "linux", true},
		{"linux arm64", domain.PlatformLinux, "arm64", "arm", true},
		{"windows amd64", domain.PlatformWindows, "amd64", "windows", true},
		{"windows arm64", domain.PlatformWindows, "arm64", "", false},
		{"darwin amd64", domain.PlatformDarwin, "amd64", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installer := patcherInstaller{platform: tt.platform, arch: tt.arch}

			_, got, ok := installer.pickAsset(assets)
			assert.Equal(t, tt.wantOK, ok, "picked an asset: %q", got)

			if tt.wantOK {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// patcherServer serves a release document and its asset, counting requests.
func patcherServer(t *testing.T, assetName string, archive []byte) (*httptest.Server, *int) {
	t.Helper()

	hits := 0

	var assetURL string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++

		switch r.URL.Path {
		case "/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "0.1.8",
				"assets": []map[string]string{{
					"name":                 assetName,
					"browser_download_url": assetURL,
				}},
			})
		case "/asset":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))

	assetURL = server.URL + "/asset"

	t.Cleanup(server.Close)

	return server, &hits
}

func TestInstallPatcherDownloadsTheLatestRelease(t *testing.T) {
	payload := []byte("#!/bin/sh\necho patched\n")
	name := "dtkit-patch-0.1.8-" + patcherAssetArchTokens(runtime.GOARCH)[0] +
		"-unknown-linux-musl.tar.gz"

	server, hits := patcherServer(t, name, patcherTarGz(t, "dtkit-patch", payload))

	patcherReleaseURL = server.URL + "/releases/latest"
	t.Cleanup(func() { patcherReleaseURL = defaultPatcherReleaseURL })

	game := t.TempDir()

	result, err := installPatcher(context.Background(), domain.ActionContext{
		Platform:    domain.PlatformLinux,
		InstallPath: game,
	})
	require.NoError(t, err, "install patcher")
	require.NotNil(t, result, "result")
	require.True(t, result.Success, "install failed: %+v", result)
	assert.Equal(t, "actions.darktide.installPatcher.installed", result.MessageKey)

	data, ok := result.Data.(map[string]any)
	require.True(t, ok, "result data is %T", result.Data)
	assert.Equal(t, "0.1.8", data["version"])

	installed := filepath.Join(game, "tools", "dtkit-patch")

	got, err := os.ReadFile(installed)
	require.NoError(t, err, "read the installed patcher")
	assert.Equal(t, payload, got)

	info, err := os.Stat(installed)
	require.NoError(t, err, "stat the installed patcher")
	assert.NotZero(t, info.Mode()&0o111, "installed patcher is not executable")
	assert.Equal(t, 2, *hits, "expected a release lookup and one download")

	again, err := installPatcher(context.Background(), domain.ActionContext{
		Platform:    domain.PlatformLinux,
		InstallPath: game,
	})
	require.NoError(t, err, "install patcher again")
	require.NotNil(t, again, "result")
	assert.True(t, again.Success, "second install failed: %+v", again)
	assert.Equal(t, "actions.darktide.installPatcher.alreadyInstalled", again.MessageKey)
	assert.Equal(t, 2, *hits, "an installed patcher still reached GitHub")
}

func TestInstallPatcherReportsAMissingAsset(t *testing.T) {
	server, _ := patcherServer(t, "dtkit-patch-0.1.8-x86_64-pc-windows-msvc.zip", nil)

	patcherReleaseURL = server.URL + "/releases/latest"
	t.Cleanup(func() { patcherReleaseURL = defaultPatcherReleaseURL })

	result, err := installPatcher(context.Background(), domain.ActionContext{
		Platform:    domain.PlatformLinux,
		InstallPath: t.TempDir(),
	})
	require.NoError(t, err, "install patcher")
	require.NotNil(t, result, "result")
	assert.False(t, result.Success, "missing asset reported success")
	assert.Equal(t, "actions.darktide.installPatcher.noAsset", result.MessageKey)
}

func TestPatcherPayloadFindsTheWindowsBinary(t *testing.T) {
	payload := []byte("MZ")

	got, err := patcherPayload("release.zip", patcherZip(t, "dtkit-patch.exe", payload), "dtkit-patch.exe")
	require.NoError(t, err, "unzip")
	assert.Equal(t, payload, got)
}

func TestPatcherPayloadNeedsTheBinary(t *testing.T) {
	archive := patcherTarGz(t, "README.md", []byte("hi"))

	_, err := patcherPayload("release.tar.gz", archive, "dtkit-patch")
	assert.Error(t, err, "unpacked an archive without the patcher")
}
