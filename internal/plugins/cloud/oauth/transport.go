package oauth

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// transport performs authenticated calls against one provider's API. The access
// token is attached per request.
type transport struct {
	session *Session
	apiBase string
}

func (t transport) newRequest(
	ctx context.Context,
	method, rawURL string,
	body io.Reader,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"build %s %s", method, rawURL)
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	return req, nil
}

// authorize attaches the token. It refreshes when the current one has run out,
// which is why it can fail before the request is ever sent.
func (t transport) authorize(ctx context.Context, req *http.Request) error {
	token, err := t.session.Access(ctx)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token)

	return nil
}

// raw sends a request and returns the body. A body is always read, even on a
// failure status, because that is where a provider writes the reason.
func (t transport) raw(
	ctx context.Context,
	method, rawURL string,
	body io.Reader,
	header http.Header,
) ([]byte, http.Header, error) {
	if t.session == nil {
		return nil, nil, domain.ErrCloudAuthRequired
	}

	req, err := t.newRequest(ctx, method, rawURL, body)
	if err != nil {
		return nil, nil, err
	}

	if err := t.authorize(ctx, req); err != nil {
		return nil, nil, err
	}

	for name, values := range header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}

	resp, err := t.http().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}

		return nil, nil, appErrors.Wrap(err, appErrors.ErrCodeSourceUnavailable,
			"reach %s", rawURL)
	}

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit))
	if err != nil {
		return nil, nil, appErrors.Wrap(err, appErrors.ErrCodeSourceUnavailable,
			"read the response from %s", rawURL)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header, appErrors.Wrap(&AuthError{
			Reason: reasonFrom(resp.StatusCode, raw),
		}, appErrors.ErrCodeCloudDownloadFailed, "%s %s", method, rawURL)
	}

	return raw, resp.Header, nil
}

// put streams a local file to rawURL. The file is streamed, not buffered:
// a share archive is as large as the mods it holds, and buffering one is how a
// manager exhausts the memory of a machine about to hand over a 4 GB mod.
func (t transport) put(
	ctx context.Context,
	method, rawURL, localPath string,
	header http.Header,
	size int64,
) (http.Header, error) {
	if t.session == nil {
		return nil, domain.ErrCloudAuthRequired
	}

	f, err := os.Open(filepath.Clean(localPath))
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"open %q to upload", localPath)
	}

	defer func() { _ = f.Close() }()

	req, err := t.newRequest(ctx, method, rawURL, f)
	if err != nil {
		return nil, err
	}

	req.ContentLength = size

	if err := t.authorize(ctx, req); err != nil {
		return nil, err
	}

	for name, values := range header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}

	resp, err := t.http().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		return nil, appErrors.Wrap(err, appErrors.ErrCodeCloudUploadFailed,
			"upload to %s", rawURL)
	}

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit))
	if err != nil {
		return nil, appErrors.Wrap(err, appErrors.ErrCodeCloudUploadFailed,
			"read the response from %s", rawURL)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, appErrors.Wrap(&AuthError{
			Reason: reasonFrom(resp.StatusCode, raw),
		}, appErrors.ErrCodeCloudUploadFailed, "upload to %s", rawURL)
	}

	// A resumable or multipart upload answers with the file it accepted; one that
	// does not may leave the file where it was asked to be, the only id there is.
	return resp.Header, nil
}

// get streams a provider object into destPath.
func (t transport) get(ctx context.Context, rawURL, destPath string) error {
	if t.session == nil {
		return domain.ErrCloudAuthRequired
	}

	req, err := t.newRequest(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}

	if err := t.authorize(ctx, req); err != nil {
		return err
	}

	resp, err := t.http().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		return appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"download from %s", rawURL)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, responseLimit))

		return appErrors.Wrap(&AuthError{Reason: reasonFrom(resp.StatusCode, raw)},
			appErrors.ErrCodeCloudDownloadFailed, "download from %s", rawURL)
	}

	return writeDownload(resp.Body, destPath)
}

// writeDownload streams an open response body into destPath.
//
// The bytes land on a sibling temporary name and are moved into place, so a
// failed transfer never leaves something that looks like the archive the payload
// described — a hash mismatch must be the provider's fault, not our file.
func writeDownload(body io.Reader, destPath string) error {
	parent := filepath.Dir(destPath)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"create %q", parent)
	}

	tmp, err := os.CreateTemp(parent, ".dmm-cloud-*")
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"create a staging file for %q", destPath)
	}

	tmpName := tmp.Name()
	committed := false

	defer func() {
		_ = tmp.Close()

		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmp, body); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"write the download into %q", destPath)
	}

	if err := tmp.Close(); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"close the download into %q", destPath)
	}

	if err := os.Rename(tmpName, destPath); err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"move the download into %q", destPath)
	}

	committed = true

	return nil
}

// http is the shared client. A generous timeout matches what an upload of a
// mod archive actually takes on a slow connection; cancellation is the
// context's job, not the timeout's.
func (t transport) http() *http.Client {
	return &http.Client{Timeout: 30 * time.Minute}
}

// call sends a request whose body is already rendered, and reads the response.
func (t transport) call(
	ctx context.Context,
	method, rawURL string,
	body io.Reader,
	header http.Header,
) (*Result, error) {
	got, gotHeader, err := t.raw(ctx, method, rawURL, body, header)
	if err != nil {
		return nil, err
	}

	return &Result{Body: got, Header: gotHeader}, nil
}

// getWithHeaders is get with the headers an API needs next to the token.
func (t transport) getWithHeaders(
	ctx context.Context,
	rawURL, destPath string,
	header http.Header,
) error {
	if t.session == nil {
		return domain.ErrCloudAuthRequired
	}

	req, err := t.newRequest(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}

	if err := t.authorize(ctx, req); err != nil {
		return err
	}

	for name, values := range header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}

	resp, err := t.http().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		return appErrors.Wrap(err, appErrors.ErrCodeCloudDownloadFailed,
			"download from %s", rawURL)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, responseLimit))

		return appErrors.Wrap(&AuthError{Reason: reasonFrom(resp.StatusCode, raw)},
			appErrors.ErrCodeCloudDownloadFailed, "download from %s", rawURL)
	}

	return writeDownload(resp.Body, destPath)
}

// putRange streams a local file into an open upload session in fragments. The
// ranges are read from the file, not a buffer: an archive is as large as
// the mods it holds, and a session exists so the service reassembles them.
func (t transport) putRange(
	ctx context.Context,
	uploadURL, localPath string,
	total int64,
	onBody func([]byte),
	fragment int64,
) error {
	if t.session == nil {
		return domain.ErrCloudAuthRequired
	}

	f, err := os.Open(filepath.Clean(localPath))
	if err != nil {
		return appErrors.Wrap(err, appErrors.ErrCodeInvalidArgument,
			"open %q to upload", localPath)
	}

	defer func() { _ = f.Close() }()

	if fragment <= 0 {
		fragment = 60 * 1024 * 1024
	}

	buf := make([]byte, fragment)

	var offset int64

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		n, readErr := io.ReadFull(f, buf)
		if n == 0 {
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				return nil
			}

			if readErr != nil {
				return appErrors.Wrap(readErr, appErrors.ErrCodeCloudUploadFailed,
					"read %q", localPath)
			}
		}

		chunk := buf[:n]
		end := offset + int64(n)

		req, err := t.newRequest(ctx, http.MethodPut, uploadURL, bytes.NewReader(chunk))
		if err != nil {
			return err
		}

		if err := t.authorize(ctx, req); err != nil {
			return err
		}

		req.ContentLength = int64(n)
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end-1, total))

		resp, err := t.http().Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}

			return appErrors.Wrap(err, appErrors.ErrCodeCloudUploadFailed,
				"send bytes %d-%d of %q", offset, end-1, localPath)
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, responseLimit))
		_ = resp.Body.Close()

		if readErr != nil {
			return appErrors.Wrap(readErr, appErrors.ErrCodeCloudUploadFailed,
				"read the upload response")
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return appErrors.Wrap(&AuthError{Reason: reasonFrom(resp.StatusCode, body)},
				appErrors.ErrCodeCloudUploadFailed, "send bytes %d-%d of %q",
				offset, end-1, localPath)
		}

		if onBody != nil && len(body) > 0 {
			onBody(body)
		}

		offset = end

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			return nil
		}
	}
}
