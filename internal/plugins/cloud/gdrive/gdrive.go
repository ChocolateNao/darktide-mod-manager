// Package gdrive is the Google Drive adapter.
//
// Drive files a share as archives inside one folder, and the name each archive
// gets is the id the payload carries: no second lookup table has to be kept in
// step with the names Drive already holds.
package gdrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/internal/plugins/cloud/oauth"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
)

const ProviderID = "gdrive"

const apiBase = "https://www.googleapis.com/drive/v3"

const uploadBase = "https://www.googleapis.com/upload/drive/v3"

// Scope covers creating a share and removing it. drive.file is used in
// preference to full access because it only reaches files this app made, which
// is exactly the set a share touches.
const Scope = "https://www.googleapis.com/auth/drive.file"

type Provider struct {
	session  *oauth.Session
	folderID string
	clientID string
}

type Config struct {
	ClientID     string
	ClientSecret string
	FolderID     string
}

var _ domain.CloudProvider = (*Provider)(nil)

func New(cfg Config) (*Provider, error) {
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("%s requires a client id", ProviderID)
	}

	if cfg.FolderID == "" {
		return nil, fmt.Errorf("%s requires a folder id to store shares in", ProviderID)
	}

	p := &Provider{
		folderID: cfg.FolderID,
		clientID: cfg.ClientID,
	}

	p.session = oauth.New(oauth.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoints: oauth.Endpoints{
			TokenURL:   "https://oauth2.googleapis.com/token",
			ConsentURL: "https://accounts.google.com/o/oauth2/v2/auth",
		},
	})

	return p, nil
}

func (p *Provider) ID() string          { return ProviderID }
func (p *Provider) Name() string        { return "Google Drive" }
func (p *Provider) Description() string { return "Shares stored as archives in a Drive folder" }
func (p *Provider) SupportsAuth() bool  { return true }
func (p *Provider) MaxFileSize() int64  { return 5 * 1024 * 1024 * 1024 }
func (p *Provider) MaxTotalSize() int64 { return 0 }

// ConsentURL is the page the user approves on; the GUI opens it, a provider
// never opens a browser itself.
func (p *Provider) ConsentURL(redirectURL, state string) string {
	if p == nil {
		return ""
	}

	return p.session.ConsentURL(redirectURL, state, Scope)
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

// driveFile is the metadata Drive keeps for one archive.
type driveFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"-"`
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
		size := f.Size
		if size <= 0 {
			info, err := os.Stat(f.Source)
			if err != nil {
				return nil, fmt.Errorf("measure %q to upload: %w", f.Source, err)
			}

			size = info.Size()
		}

		// The patch that names the file runs only after its bytes have landed;
		// naming first would leave a named but empty object behind on failure.
		endpoint := fmt.Sprintf("%s/files?uploadType=media&fields=id,name", uploadBase)

		header := http.Header{}
		header.Set("Content-Type", "application/zip")

		res, err := p.session.Put(ctx, http.MethodPost, endpoint, f.Source, header, size)
		if err != nil {
			return nil, fmt.Errorf("upload %q: %w", f.Path, err)
		}

		var created driveFile
		if err := json.Unmarshal(res.Body, &created); err != nil {
			return nil, fmt.Errorf("read the upload response for %q: %w", f.Path, err)
		}

		if created.ID == "" {
			return nil, fmt.Errorf("%s returned no id for %q", ProviderID, f.Path)
		}

		if err := p.moveIntoFolder(ctx, created.ID, f.Path); err != nil {
			return nil, err
		}

		refs = append(refs, domain.FileReference{ID: f.Path, Size: size, Hash: f.Hash})
	}

	return &domain.UploadResult{References: refs}, nil
}

// moveIntoFolder names and files an uploaded object.
//
// Drive drops an upload at the root of the account unless a parent is given, so
// the rename and the parent land in one patch: a root file named after a payload
// id is one the folder-scoped lookupID will never find.
func (p *Provider) moveIntoFolder(ctx context.Context, id, name string) error {
	endpoint := fmt.Sprintf("%s/files/%s?fields=id,name", apiBase, id)

	payload := map[string]any{"name": name, "parents": []string{p.folderID}}

	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	header := http.Header{}
	header.Set("Content-Type", "application/json")

	if _, err := p.session.Send(ctx, http.MethodPatch, endpoint, raw, header); err != nil {
		return fmt.Errorf("file the uploaded archive %q: %w", name, err)
	}

	return nil
}

func (p *Provider) Download(ctx context.Context, ref domain.FileReference, destPath string) error {
	if p == nil {
		return domain.ErrCloudProviderNotFound
	}

	id, err := p.lookupID(ctx, ref.ID)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/files/%s?alt=media", apiBase, id)

	return p.session.Get(ctx, endpoint, destPath)
}

// lookupID searches the share folder for the archive a name refers to.
//
// Drive names are unique per folder rather than per account, so the search is
// scoped to the share folder; two archives of one name there have already made
// the share ambiguous, and the first match is as good as any.
func (p *Provider) lookupID(ctx context.Context, name string) (string, error) {
	clause := fmt.Sprintf(
		"name = '%s' and '%s' in parents and trashed = false",
		escapeName(name), p.folderID,
	)

	endpoint := fmt.Sprintf("%s/files?q=%s&fields=files(id,name)",
		apiBase, url.QueryEscape(clause))

	res, err := p.session.Send(ctx, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return "", fmt.Errorf("find %q in the share folder: %w", name, err)
	}

	var listing struct {
		Files []driveFile `json:"files"`
	}

	if err := json.Unmarshal(res.Body, &listing); err != nil {
		return "", fmt.Errorf("read the folder listing: %w", err)
	}

	if len(listing.Files) == 0 {
		return "", fmt.Errorf("%s is not in the share folder", name)
	}

	return listing.Files[0].ID, nil
}

func (p *Provider) DeleteFile(ctx context.Context, ref domain.FileReference) error {
	if p == nil {
		return domain.ErrCloudProviderNotFound
	}

	id, err := p.lookupID(ctx, ref.ID)
	if err != nil {
		// A file that is already gone is what a revoke wants to hear.
		return nil
	}

	endpoint := fmt.Sprintf("%s/files/%s", apiBase, id)

	if _, err := p.session.Send(ctx, http.MethodDelete, endpoint, nil, nil); err != nil {
		return fmt.Errorf("delete %q: %w", ref.ID, err)
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
		URL:        fmt.Sprintf("gdrive://%s/%s", p.folderID, name),
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

	prefix := fmt.Sprintf("gdrive://%s/", p.folderID)

	name := link.URL
	switch {
	case strings.HasPrefix(name, prefix):
		name = strings.TrimPrefix(name, prefix)
	case strings.Contains(name, "/"):
		name = path.Base(name)
	}

	id, err := p.lookupID(ctx, name)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/files/%s?fields=id,name,size", apiBase, id)

	res, err := p.session.Send(ctx, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", name, err)
	}

	var found driveFile
	if err := json.Unmarshal(res.Body, &found); err != nil {
		return nil, fmt.Errorf("read the metadata of %q: %w", name, err)
	}

	found.Size = parseSize(res.Header.Get("Content-Length"))

	return []domain.FileReference{{ID: name, Size: found.Size}}, nil
}

// escapeName quotes a name for Drive's query language. A name holding a quote
// would otherwise end the literal early and match everything after it.
func escapeName(name string) string {
	return strings.ReplaceAll(name, `'`, `\'`)
}

// parseSize reads a byte count out of a header. Drive answers the metadata call
// with the size only when asked for it, so an empty value is normal, not an error.
func parseSize(raw string) int64 {
	var size int64

	if raw == "" {
		return 0
	}

	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0
		}

		size = size*10 + int64(r-'0')
	}

	return size
}
