package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Doer performs one HTTP request. *http.Client satisfies it, and a test
// supplies its own: no test in this repository may reach the network
// (docs/lessons.md), and every response below — a release, a checksums file, a
// truncated archive — is a value a test can construct.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// DefaultRepo is the repository avar updates itself from.
const DefaultRepo = "olamide226/avar"

// Limits on what avar will read from the network. The release document and the
// checksums file are small and known; the archive cap is well above a release
// (about 12 MiB in v0.12.12) and exists so that a wrong URL or a hostile
// response cannot fill the disk before the checksum has a chance to refuse it.
const (
	maxReleaseBytes   = 4 << 20
	maxChecksumBytes  = 1 << 20
	maxArchiveBytes   = 256 << 20
	acceptGitHubJSON  = "application/vnd.github+json"
	acceptOctetStream = "application/octet-stream"
)

// Asset is one file published with a release.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Release is the part of a GitHub release avar reads.
type Release struct {
	// Tag is the release tag, such as "v1.4.0".
	Tag string
	// Assets are the files published with it, in the order GitHub lists
	// them.
	Assets []Asset
}

// Version is the release's version, parsed from its tag.
func (r Release) Version() (Version, error) {
	v, err := ParseVersion(r.Tag)
	if err != nil {
		return Version{}, fmt.Errorf("read the version of release %q: %w", r.Tag, err)
	}
	return v, nil
}

// Asset finds one published file by exact name.
func (r Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// AssetNames lists what the release published, for an error that has to say
// what was there instead of what was wanted.
func (r Release) AssetNames() []string {
	names := make([]string, 0, len(r.Assets))
	for _, a := range r.Assets {
		names = append(names, a.Name)
	}
	return names
}

// releaseDocument is GitHub's release JSON, narrowed to what avar reads.
type releaseDocument struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// LatestRelease fetches the newest published release of a repository.
//
// `releases/latest` is the newest release that is neither a draft nor a
// prerelease, which is what avar wants: a prerelease must never offer itself
// as an update to somebody who installed a stable one.
func LatestRelease(ctx context.Context, doer Doer, repo, userAgent string) (Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	body, err := get(ctx, doer, url, userAgent, acceptGitHubJSON, maxReleaseBytes)
	if err != nil {
		return Release{}, fmt.Errorf("ask github.com/%s for its latest release: %w", repo, err)
	}

	var doc releaseDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return Release{}, fmt.Errorf("read the latest release of github.com/%s: %w", repo, err)
	}
	if doc.TagName == "" {
		return Release{}, fmt.Errorf("github.com/%s reported a release with no tag, so there is no version to compare against", repo)
	}

	release := Release{Tag: doc.TagName}
	for _, a := range doc.Assets {
		release.Assets = append(release.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
	}
	return release, nil
}

// FetchChecksums downloads and parses a release's checksums.txt.
func FetchChecksums(ctx context.Context, doer Doer, a Asset, userAgent string) (map[string]string, error) {
	body, err := get(ctx, doer, a.URL, userAgent, acceptOctetStream, maxChecksumBytes)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", a.Name, err)
	}
	sums, err := ParseChecksums(body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", a.Name, err)
	}
	return sums, nil
}

// ErrChecksumMismatch is returned when a download is not the file the release
// says it is — because it was altered, because it ended early, or because the
// wrong thing was served. All three mean the same thing to a program about to
// execute it.
var ErrChecksumMismatch = errors.New("the download does not match the checksum the release published for it")

// DownloadVerified copies an asset into w and returns an error unless what
// arrived hashes to want.
//
// The hash is computed as the bytes stream past, so nothing is held in memory
// and nothing is written anywhere avar would run it from: the caller's writer
// is a temporary file, and unpacking happens only after this returns nil
// (REQ-19.5, PROP-26).
func DownloadVerified(ctx context.Context, doer Doer, a Asset, userAgent, want string, w io.Writer) error {
	want = strings.ToLower(strings.TrimSpace(want))
	if len(want) != sha256.Size*2 {
		return fmt.Errorf("download %s: %q is not a SHA-256 checksum, so the download could not be verified: %w", a.Name, want, ErrChecksumMismatch)
	}

	body, err := open(ctx, doer, a.URL, userAgent, acceptOctetStream)
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer body.Close()

	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, digest), io.LimitReader(body, maxArchiveBytes+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	if n > maxArchiveBytes {
		return fmt.Errorf("download %s: the file is larger than the %d MiB avar will download for an update", a.Name, maxArchiveBytes>>20)
	}
	// A body that stops early is the failure this names before the hashes
	// are compared, because "4 MiB of 12 MiB arrived" tells the user to try
	// again, and "the checksums differ" makes them wonder who altered it.
	if a.Size > 0 && n != a.Size {
		return fmt.Errorf("download %s: the download ended after %d of %d bytes: %w", a.Name, n, a.Size, ErrChecksumMismatch)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != want {
		return fmt.Errorf("download %s: it hashes to %s and the release published %s: %w", a.Name, got, want, ErrChecksumMismatch)
	}
	return nil
}

// get reads a whole response body, up to max bytes.
func get(ctx context.Context, doer Doer, url, userAgent, accept string, max int64) ([]byte, error) {
	body, err := open(ctx, doer, url, userAgent, accept)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	read, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(read)) > max {
		return nil, fmt.Errorf("the reply is larger than the %d bytes avar will read for this", max)
	}
	return read, nil
}

// open performs the request and returns the body of a successful reply.
//
// A status avar cannot use is an error naming the status, and 403 names the
// rate limit, because that is the one a user meets without having done
// anything wrong and the one with an obvious next step.
func open(ctx context.Context, doer Doer, url, userAgent, accept string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build a request for %s: %w", url, err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", userAgent)

	resp, err := doer.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}

	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, fmt.Errorf("%s replied 404: there is no release to read there", url)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("%s replied %s, which is usually GitHub's rate limit for unauthenticated requests: try again later, or download the release yourself", url, resp.Status)
	default:
		return nil, fmt.Errorf("%s replied %s", url, resp.Status)
	}
}
