// Package onedrive is the OneDrive adapter.
//
// OneDrive addresses a file by the path under the drive root, so the id the
// payload carries is that path. An upload session is what makes a large archive
// survivable: a dropped connection resumes from a fragment instead of over.
package onedrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/oauth"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

const ProviderID = "onedrive"

const apiBase = "https://graph.microsoft.com/v1.0"

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
		folder = "DarktideModManager"
	}

	p := &Provider{folder: path.Clean("/" + folder)}

	p.session = oauth.New(oauth.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoints: oauth.Endpoints{
			TokenURL:   "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
			ConsentURL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize",
		},
	})

	return p, nil
}

func (p *Provider) ID() string   { return ProviderID }
func (p *Provider) Name() string { return "OneDrive" }
func (p *Provider) Description() string {
	return "Shares stored as archives in a OneDrive folder"
}
func (p *Provider) SupportsAuth() bool  { return true }
func (p *Provider) MaxFileSize() int64  { return 100 * 1024 * 1024 * 1024 }
func (p *Provider) MaxTotalSize() int64 { return 0 }

// ConsentURL is the page the user approves on; the GUI opens it, a provider
// never opens a browser itself.
func (p *Provider) ConsentURL(redirectURL, state string) string {
	if p == nil {
		return ""
	}

	// Files.ReadWrite reaches only what this app puts there, which is exactly the
	// set a share touches.
	return p.session.ConsentURL(redirectURL, state, "Files.ReadWrite offline_access")
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

// objectPath is where an archive named by the payload lives.
func (p *Provider) objectPath(name string) string {
	if name == "" {
		return fmt.Sprintf("%s/root:%s", apiBase, p.folder)
	}

	return fmt.Sprintf("%s/root:%s/%s", apiBase, p.folder, name)
}

// item is the metadata OneDrive reports for one file.
type item struct {
	File *struct {
		Hashes map[string]string `json:"hashes"`
	} `json:"file"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
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
		// An upload session is opened first and the bytes follow in fragments: a
		// one-shot PUT buffers the whole archive and gives up when the link drops.
		uploadURL, err := p.createSession(ctx, p.objectPath(f.Path))
		if err != nil {
			return nil, fmt.Errorf("upload %q: %w", f.Path, err)
		}

		stored, err := p.sendFragments(ctx, uploadURL, f)
		if err != nil {
			return nil, fmt.Errorf("upload %q: %w", f.Path, err)
		}

		refs = append(refs, domain.FileReference{
			ID:   f.Path,
			Size: stored.Size,
			Hash: f.Hash,
		})
	}

	return &domain.UploadResult{References: refs}, nil
}

// fragmentSize is one slice of an archive. 60 MiB is the largest OneDrive
// accepts for a fragment that is not the last, and a multiple of 320 KiB, which
// is what the service requires of every fragment but the final one.
const fragmentSize = 60 * 1024 * 1024

// createSession opens the upload OneDrive will reassemble.
func (p *Provider) createSession(ctx context.Context, target string) (string, error) {
	payload := map[string]any{
		"item": map[string]string{"@microsoft.graph.conflictBehavior": "replace"},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	header := make(map[string][]string)
	header["Content-Type"] = []string{"application/json"}

	endpoint := target + ":/createUploadSession"

	res, err := p.session.Send(ctx, http.MethodPost, endpoint, raw, header)
	if err != nil {
		return "", err
	}

	var session struct {
		UploadURL string `json:"uploadUrl"`
	}

	if err := json.Unmarshal(res.Body, &session); err != nil {
		return "", fmt.Errorf("read the upload session: %w", err)
	}

	if session.UploadURL == "" {
		return "", fmt.Errorf("%s returned no upload session", ProviderID)
	}

	return session.UploadURL, nil
}

// sendFragments streams a local file into an open upload session.
func (p *Provider) sendFragments(
	ctx context.Context,
	uploadURL string,
	f domain.ShareFile,
) (item, error) {
	size := f.Size
	if size <= 0 {
		info, err := os.Stat(f.Source)
		if err != nil {
			return item{}, fmt.Errorf("measure %q: %w", f.Source, err)
		}

		size = info.Size()
	}

	var last item

	err := p.session.PutRange(ctx, uploadURL, f.Source, size, func(b []byte) {
		_ = json.Unmarshal(b, &last)
	})
	if err != nil {
		return item{}, err
	}

	if last.ID == "" {
		// The final fragment answers with the created item, so its id is what
		// proves the file landed.
		return item{}, fmt.Errorf("%s completed the upload but returned no file", ProviderID)
	}

	return last, nil
}

func (p *Provider) Download(ctx context.Context, ref domain.FileReference, destPath string) error {
	if p == nil {
		return domain.ErrCloudProviderNotFound
	}

	endpoint := fmt.Sprintf("%s/root:%s/%s:/content", apiBase, p.folder, ref.ID)

	return p.session.Get(ctx, endpoint, destPath)
}

func (p *Provider) DeleteFile(ctx context.Context, ref domain.FileReference) error {
	if p == nil {
		return domain.ErrCloudProviderNotFound
	}

	endpoint := fmt.Sprintf("%s/root:%s/%s", apiBase, p.folder, ref.ID)

	if _, err := p.session.Send(ctx, http.MethodDelete, endpoint, nil, nil); err != nil {
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
		URL:        fmt.Sprintf("onedrive://%s/%s", p.folder, name),
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

	prefix := fmt.Sprintf("onedrive://%s/", p.folder)

	name := link.URL
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		name = name[len(prefix):]
	} else {
		name = path.Base(name)
	}

	endpoint := fmt.Sprintf("%s/root:%s/%s", apiBase, p.folder, name)

	res, err := p.session.Send(ctx, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", name, err)
	}

	var found item
	if err := json.Unmarshal(res.Body, &found); err != nil {
		return nil, fmt.Errorf("read the metadata of %q: %w", name, err)
	}

	return []domain.FileReference{{ID: name, Size: found.Size}}, nil
}
