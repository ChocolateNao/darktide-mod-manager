// Package dropbox is the Dropbox adapter.
//
// Dropbox addresses a file by its full path, so the id the payload carries is
// the path the archive was uploaded to; the same string names the upload,
// downloads it again, and deletes it.
package dropbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/oauth"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

const ProviderID = "dropbox"

const apiBase = "https://api.dropboxapi.com/2"

const contentBase = "https://content.dropboxapi.com/2"

type Provider struct {
	session *oauth.Session
	folder  string
}

type Config struct {
	ClientID     string
	ClientSecret string
	Folder       string
}

var _ domain.CloudProvider = (*Provider)(nil)

func New(cfg Config) (*Provider, error) {
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("%s requires a client id", ProviderID)
	}

	folder := cfg.Folder
	if folder == "" {
		folder = "/DarktideModManager"
	}

	p := &Provider{folder: path.Clean("/" + folder)}

	p.session = oauth.New(oauth.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoints: oauth.Endpoints{
			TokenURL:   "https://api.dropboxapi.com/oauth2/token",
			ConsentURL: "https://www.dropbox.com/oauth2/authorize",
		},
	})

	return p, nil
}

func (p *Provider) ID() string   { return ProviderID }
func (p *Provider) Name() string { return "Dropbox" }
func (p *Provider) Description() string {
	return "Shares stored as archives in a Dropbox folder"
}
func (p *Provider) SupportsAuth() bool  { return true }
func (p *Provider) MaxFileSize() int64  { return 350 * 1024 * 1024 * 1024 }
func (p *Provider) MaxTotalSize() int64 { return 0 }

// ConsentURL is the page the user approves on; the GUI opens it, a provider
// never opens a browser itself.
func (p *Provider) ConsentURL(redirectURL, state string) string {
	if p == nil {
		return ""
	}

	return p.session.ConsentURL(redirectURL, state, "")
}

// Authorize exchanges a consent code for a token.
func (p *Provider) Authorize(ctx context.Context, code, redirectURL string) error {
	if p == nil {
		return domain.ErrCloudAuthRequired
	}

	return p.session.Authorize(ctx, code, redirectURL)
}

func (p *Provider) Authenticate(creds domain.CloudCredentials) error {
	if p == nil {
		return domain.ErrCloudAuthRequired
	}

	return p.session.Authenticate(creds)
}

func (p *Provider) ValidateCredentials(ctx context.Context) error {
	if p == nil {
		return domain.ErrCloudAuthRequired
	}

	return p.session.ValidateCredentials(ctx)
}

func (p *Provider) ForgetCredentials() {
	if p == nil {
		return
	}

	p.session.Forget()
}

// objectPath is where an archive named by the payload lives. A name carrying a
// directory is honoured rather than flattened, so two mods shipping the same
// mod_info.lua do not collide inside one folder.
func (p *Provider) objectPath(name string) string {
	if name == "" {
		return p.folder
	}

	return path.Join(p.folder, name)
}

// entry is the metadata Dropbox reports for one file.
type entry struct {
	Name          string `json:"name"`
	PathDisplay   string `json:"path_display"`
	PathLower     string `json:"path_lower"`
	ContentHash   string `json:"content_hash"`
	ServerModTime string `json:"server_modified"`
	Size          int64  `json:"size"`
}

func (p *Provider) Upload(
	ctx context.Context,
	files []domain.ShareFile,
	_ domain.UploadOptions,
) (*domain.UploadResult, error) {
	if p == nil {
		return nil, domain.ErrCloudProviderNotFound
	}

	refs := make([]domain.FileReference, 0, len(files))

	for _, f := range files {
		target := p.objectPath(f.Path)

		// The destination rides in an API-arg header while the bytes go in the body.
		// Overwriting is deliberate: re-sharing after an edit must not fail on exists.
		arg, err := json.Marshal(map[string]any{"path": target, "mode": "overwrite"})
		if err != nil {
			return nil, err
		}

		endpoint := contentBase + "/files/upload"

		header := make(map[string][]string)
		header["Dropbox-API-Arg"] = []string{string(arg)}
		header["Content-Type"] = []string{"application/octet-stream"}

		res, err := p.session.Put(ctx, http.MethodPost, endpoint, f.Source, header, f.Size)
		if err != nil {
			return nil, fmt.Errorf("upload %q: %w", f.Path, err)
		}

		var stored entry
		if err := json.Unmarshal(res.Body, &stored); err != nil {
			return nil, fmt.Errorf("read the upload response for %q: %w", f.Path, err)
		}

		refs = append(refs, domain.FileReference{
			ID:   f.Path,
			Size: stored.Size,
			Hash: f.Hash,
		})
	}

	return &domain.UploadResult{References: refs}, nil
}

func (p *Provider) Download(ctx context.Context, ref domain.FileReference, destPath string) error {
	if p == nil {
		return domain.ErrCloudProviderNotFound
	}

	arg, err := json.Marshal(map[string]string{"path": p.objectPath(ref.ID)})
	if err != nil {
		return err
	}

	header := make(map[string][]string)
	header["Dropbox-API-Arg"] = []string{string(arg)}

	endpoint := contentBase + "/files/download"

	return p.session.GetWithHeaders(ctx, endpoint, destPath, header)
}

func (p *Provider) DeleteFile(ctx context.Context, ref domain.FileReference) error {
	if p == nil {
		return domain.ErrCloudProviderNotFound
	}

	if _, err := p.session.SendJSON(ctx, http.MethodPost, apiBase+"/files/delete_v2",
		map[string]string{"path": p.objectPath(ref.ID)}); err != nil {
		// A file that is already gone is what a revoke wants to hear.
		if oauth.IsMissing(err) {
			return nil
		}

		return err
	}

	return nil
}

func (p *Provider) CreateShareLink(
	_ context.Context,
	files []domain.FileReference,
	opts domain.ShareOptions,
) (*domain.ShareLink, error) {
	if p == nil {
		return nil, domain.ErrCloudProviderNotFound
	}

	var expiry *time.Time

	if !opts.Expiry.IsZero() {
		e := opts.Expiry
		expiry = &e
	}

	name := ""
	if len(files) > 0 {
		name = files[0].ID
	}

	return &domain.ShareLink{
		URL:        fmt.Sprintf("dropbox://%s", p.objectPath(name)),
		ProviderID: ProviderID,
		ExpiresAt:  expiry,
	}, nil
}

func (p *Provider) ResolveShareLink(
	ctx context.Context,
	link domain.ShareLink,
) ([]domain.FileReference, error) {
	if p == nil {
		return nil, domain.ErrCloudProviderNotFound
	}

	name := link.URL
	if len(name) > len("dropbox://") && name[:len("dropbox://")] == "dropbox://" {
		name = name[len("dropbox://"):]
	} else if idx := lastSlash(name); idx >= 0 {
		name = name[idx+1:]
	}

	res, err := p.session.SendJSON(ctx, http.MethodPost, apiBase+"/files/get_metadata",
		map[string]string{"path": p.objectPath(name)})
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", name, err)
	}

	var found entry
	if err := json.Unmarshal(res.Body, &found); err != nil {
		return nil, fmt.Errorf("read the metadata of %q: %w", name, err)
	}

	return []domain.FileReference{{ID: name, Size: found.Size}}, nil
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}

	return -1
}
