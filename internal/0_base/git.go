package base

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	retryablehttp "github.com/hashicorp/go-retryablehttp"
	"golang.org/x/sync/singleflight"
)

type (
	fileProvider interface {
		NewPath(string) (*Path, error)
		MakeDir(*Path) (bool, error)
		Create(string) (*os.File, error)
	}

	client interface {
		Do(req *retryablehttp.Request) (*http.Response, error)
	}

	GitProvider struct {
		client       client
		fileProvider fileProvider
		downloads    singleflight.Group
		// archives remembers each archive under the file it was written to, so
		// that a diagnostic naming a declaration can say which download it came
		// from rather than where on this machine it happens to sit.
		archives SyncMap[string, string]
	}
)

func NewGitProvider(fp fileProvider) *GitProvider {
	return &GitProvider{
		client:       retryablehttp.NewClient(),
		fileProvider: fp,
	}
}

// Download fetches the zip archive at archiveURL and writes it to path.
// If path already exists, the download is skipped.
//
// Concurrent calls for the same path are deduplicated: only one download
// runs and all callers receive the same result.
//
// The caller must supply a direct archive download URL for the host
// (for example a GitHub …/archive/….zip link or a GitLab
// …/api/v4/projects/…/repository/archive.zip link).
func (gp *GitProvider) Download(archiveURL string) (*Path, error) {
	parsedURL, err := url.ParseRequestURI(archiveURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse archive url %q: %w", archiveURL, err)
	}

	path := parsedURL.Host + parsedURL.Path
	absPath, err := gp.fileProvider.NewPath(path)
	if err != nil {
		return nil, err
	}
	gp.archives.Store(absPath.Outer, path)

	_, err, _ = gp.downloads.Do(path, func() (any, error) {
		if created, err := gp.fileProvider.MakeDir(absPath); err != nil {
			return nil, fmt.Errorf("failed to create parent dir for %s: %w", path, err)
		} else if !created {
			return nil, nil
		}

		req, err := retryablehttp.NewRequest(http.MethodGet, archiveURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to build request for %s: %w", archiveURL, err)
		}

		resp, err := gp.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to download %s: %w", archiveURL, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("failed to download %s: %s", archiveURL, resp.Status)
		}

		f, err := gp.fileProvider.Create(path)
		if err != nil {
			return nil, fmt.Errorf("failed to create %s: %w", path, err)
		}

		_, err = io.Copy(f, resp.Body)
		closeErr := f.Close()
		if err != nil {
			_ = os.Remove(path)
			return nil, fmt.Errorf("failed to write %s: %w", path, err)
		}
		if closeErr != nil {
			_ = os.Remove(path)
			return nil, fmt.Errorf("failed to close %s: %w", path, closeErr)
		}
		return nil, nil
	})

	return absPath, err
}

// ArchiveName answers the URL tail a downloaded archive is known by, given the
// file it was written to. A file no download wrote answers empty.
func (gp *GitProvider) ArchiveName(file string) string {
	name, _ := gp.archives.Load(file)
	return name
}
