package darktide

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

// defaultPatcherReleaseURL is GitHub's newest-release endpoint.
const defaultPatcherReleaseURL = "https://api.github.com/repos/manshanko/dtkit-patch/releases/latest"

// patcherReleaseURL is a variable so a test can point the lookup at a local
// server.
var patcherReleaseURL = defaultPatcherReleaseURL

// maxPatcherAsset keeps a broken redirect from filling memory.
const maxPatcherAsset = 64 << 20

// errNoPatcherAsset means the release has no build for this platform.
var errNoPatcherAsset = errors.New("no dtkit-patch release asset for this platform")

type patcherRelease struct {
	Version string
	Asset   string
	URL     string
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// patcherInstaller downloads a release binary from GitHub.
type patcherInstaller struct {
	client   *http.Client
	apiURL   string
	platform domain.Platform
	arch     string
}

func newPatcherInstaller(platform domain.Platform) patcherInstaller {
	return patcherInstaller{
		client:   &http.Client{Timeout: time.Minute},
		apiURL:   patcherReleaseURL,
		platform: platform,
		arch:     runtime.GOARCH,
	}
}

// latest reads the newest release and picks the asset built for this platform.
func (i patcherInstaller) latest(ctx context.Context) (patcherRelease, error) {
	var release struct {
		TagName string         `json:"tag_name"`
		Assets  []releaseAsset `json:"assets"`
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.apiURL, nil)
	if err != nil {
		return patcherRelease{}, err
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := i.client.Do(req)
	if err != nil {
		return patcherRelease{}, fmt.Errorf("query the dtkit-patch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return patcherRelease{}, fmt.Errorf("dtkit-patch releases answered %s", resp.Status)
	}

	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return patcherRelease{}, fmt.Errorf("read the dtkit-patch release: %w", err)
	}

	name, url, ok := i.pickAsset(release.Assets)
	if !ok {
		return patcherRelease{}, fmt.Errorf("%w: %s/%s", errNoPatcherAsset, i.platform, i.arch)
	}

	return patcherRelease{Version: release.TagName, Asset: name, URL: url}, nil
}

func (i patcherInstaller) pickAsset(assets []releaseAsset) (string, string, bool) {
	osToken, suffix, ok := patcherAssetFormat(i.platform)
	if !ok {
		return "", "", false
	}

	for _, asset := range assets {
		if !strings.Contains(asset.Name, osToken) || !strings.HasSuffix(asset.Name, suffix) {
			continue
		}

		for _, token := range patcherAssetArchTokens(i.arch) {
			if strings.Contains(asset.Name, token) {
				return asset.Name, asset.URL, true
			}
		}
	}

	return "", "", false
}

// patcherAssetFormat names the archive GitHub publishes for a platform.
func patcherAssetFormat(platform domain.Platform) (string, string, bool) {
	switch platform {
	case domain.PlatformLinux:
		return "linux", ".tar.gz", true
	case domain.PlatformWindows:
		return "windows", ".zip", true
	}

	return "", "", false
}

// patcherAssetArchTokens lists the spellings a Rust target uses for an
// architecture.
func patcherAssetArchTokens(arch string) []string {
	switch arch {
	case "amd64":
		return []string{"x86_64", "amd64"}
	case "arm64":
		return []string{"aarch64", "arm64"}
	}

	return []string{arch}
}

// patcherBinaryName is the file name dtkitPatchCommand looks for.
func patcherBinaryName(platform domain.Platform) string {
	if platform == domain.PlatformWindows {
		return "dtkit-patch.exe"
	}

	return "dtkit-patch"
}

// install downloads the release asset and writes its binary into dir.
func (i patcherInstaller) install(
	ctx context.Context,
	release patcherRelease,
	dir string,
) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.URL, nil)
	if err != nil {
		return "", err
	}

	resp, err := i.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", release.Asset, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", release.Asset, resp.Status)
	}

	archive, err := io.ReadAll(io.LimitReader(resp.Body, maxPatcherAsset))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", release.Asset, err)
	}

	name := patcherBinaryName(i.platform)

	payload, err := patcherPayload(release.Asset, archive, name)
	if err != nil {
		return "", err
	}

	if err := writeExecutable(dir, name, payload); err != nil {
		return "", fmt.Errorf("write %s: %w", name, err)
	}

	return filepath.Join(dir, name), nil
}

// patcherPayload pulls the patcher binary out of a release archive.
func patcherPayload(asset string, archive []byte, binary string) ([]byte, error) {
	switch {
	case strings.HasSuffix(asset, ".tar.gz"):
		return tarPayload(archive, binary)
	case strings.HasSuffix(asset, ".zip"):
		return zipPayload(archive, binary)
	}

	return nil, fmt.Errorf("dtkit-patch asset %q is not an archive this manager can unpack", asset)
}

func tarPayload(archive []byte, binary string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	reader := tar.NewReader(gz)

	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		if header.Typeflag == tar.TypeReg && filepath.Base(header.Name) == binary {
			return io.ReadAll(reader)
		}
	}

	return nil, fmt.Errorf("the archive does not contain %s", binary)
}

func zipPayload(archive []byte, binary string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}

	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() || filepath.Base(entry.Name) != binary {
			continue
		}

		file, err := entry.Open()
		if err != nil {
			return nil, err
		}
		defer file.Close()

		return io.ReadAll(file)
	}

	return nil, fmt.Errorf("the archive does not contain %s", binary)
}

// writeExecutable replaces the target atomically, so an interrupted write does
// not leave a truncated patcher behind.
func writeExecutable(dir, name string, payload []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".dtkit-patch-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
