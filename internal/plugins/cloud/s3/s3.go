package s3

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const ProviderID = "s3"

type Provider struct {
	client    *minio.Client
	endpoint  string
	region    string
	accessKey string
	secretKey string
	bucket    string
	useSSL    bool
}

type Config struct {
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

var _ domain.CloudProvider = (*Provider)(nil)

func New(cfg Config) (*Provider, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("s3 endpoint is required")
	}

	if cfg.Bucket == "" {
		return nil, errors.New("s3 bucket is required")
	}

	p := &Provider{
		endpoint:  cfg.Endpoint,
		region:    cfg.Region,
		accessKey: cfg.AccessKey,
		secretKey: cfg.SecretKey,
		bucket:    cfg.Bucket,
		useSSL:    cfg.UseSSL,
	}

	if err := p.connect(); err != nil {
		return nil, err
	}

	return p, nil
}

func (p *Provider) connect() error {
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(p.accessKey, p.secretKey, ""),
		Secure: p.useSSL,
	}
	if p.region != "" {
		opts.Region = p.region
	}

	client, err := minio.New(p.endpoint, opts)
	if err != nil {
		return fmt.Errorf("create s3 client: %w", err)
	}

	p.client = client

	return nil
}

func (p *Provider) ID() string   { return ProviderID }
func (p *Provider) Name() string { return "S3-compatible" }
func (p *Provider) Description() string {
	return "MinIO, Wasabi, Backblaze B2, or any S3-compatible object storage"
}
func (p *Provider) SupportsAuth() bool  { return true }
func (p *Provider) MaxFileSize() int64  { return 5 * 1024 * 1024 * 1024 }
func (p *Provider) MaxTotalSize() int64 { return 0 }

func (p *Provider) Authenticate(creds domain.CloudCredentials) error {
	if p == nil {
		return errors.New("nil provider")
	}

	accessKey := creds["access_key"]

	secretKey := creds["secret_key"]
	if accessKey == "" || secretKey == "" {
		return errors.New("access_key and secret_key are required")
	}

	p.accessKey = accessKey
	p.secretKey = secretKey

	return p.connect()
}

func (p *Provider) ValidateCredentials(ctx context.Context) error {
	if p == nil || p.client == nil {
		return errors.New("s3 client not initialized")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	exists, err := p.client.BucketExists(ctx, p.bucket)
	if err != nil {
		return fmt.Errorf("validate s3 credentials: %w", err)
	}

	if !exists {
		return fmt.Errorf("bucket %q does not exist", p.bucket)
	}

	return nil
}

func (p *Provider) ForgetCredentials() {
	if p == nil {
		return
	}

	p.accessKey = ""
	p.secretKey = ""
	p.client = nil
}

func (p *Provider) Upload(
	ctx context.Context,
	files []domain.ShareFile,
	_ domain.UploadOptions,
) (*domain.UploadResult, error) {
	if p == nil || p.client == nil {
		return nil, errors.New("s3 client not initialized")
	}

	var refs []domain.FileReference

	for _, f := range files {
		// The object key is the remote name; the bytes come from the local file
		// the orchestrator staged. Using one for both put the sharer's path on
		// the provider and read from a path that may not exist there.
		if f.Source == "" {
			return nil, fmt.Errorf("upload %q: no local file to read from", f.Path)
		}

		_, err := p.client.FPutObject(
			ctx,
			p.bucket,
			f.Path,
			f.Source,
			minio.PutObjectOptions{},
		)
		if err != nil {
			return nil, fmt.Errorf("upload %q: %w", f.Path, err)
		}

		refs = append(refs, domain.FileReference{
			ID:   f.Path,
			Size: f.Size,
			Hash: f.Hash,
		})
	}

	return &domain.UploadResult{References: refs}, nil
}

func (p *Provider) Download(ctx context.Context, ref domain.FileReference, destPath string) error {
	if p == nil || p.client == nil {
		return errors.New("s3 client not initialized")
	}

	return p.client.FGetObject(
		ctx,
		p.bucket,
		ref.ID,
		destPath,
		minio.GetObjectOptions{},
	)
}

func (p *Provider) DeleteFile(ctx context.Context, ref domain.FileReference) error {
	if p == nil || p.client == nil {
		return errors.New("s3 client not initialized")
	}

	return p.client.RemoveObject(
		ctx,
		p.bucket,
		ref.ID,
		minio.RemoveObjectOptions{},
	)
}

func (p *Provider) CreateShareLink(
	_ context.Context,
	files []domain.FileReference,
	opts domain.ShareOptions,
) (*domain.ShareLink, error) {
	if p == nil {
		return nil, errors.New("nil provider")
	}

	var expiry *time.Time
	if !opts.Expiry.IsZero() {
		expiry = &opts.Expiry
	}

	return &domain.ShareLink{
		URL:        fmt.Sprintf("s3://%s/%s", p.bucket, files[0].ID),
		ProviderID: ProviderID,
		ExpiresAt:  expiry,
	}, nil
}

func (p *Provider) ResolveShareLink(
	_ context.Context,
	link domain.ShareLink,
) ([]domain.FileReference, error) {
	if p == nil || p.client == nil {
		return nil, errors.New("s3 client not initialized")
	}

	prefix := fmt.Sprintf("s3://%s/", p.bucket)

	objectName := link.URL
	if len(objectName) > len(prefix) && objectName[:len(prefix)] == prefix {
		objectName = objectName[len(prefix):]
	}

	return []domain.FileReference{{ID: objectName}}, nil
}
