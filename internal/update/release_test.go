package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// No test in this package reaches the network. Every reply below is a value,
// and the one thing a real request would add — that GitHub answers at all — is
// not something a unit test can assert anyway (docs/lessons.md has two entries
// about tests that escaped into the host).
type fakeHTTP struct {
	replies  map[string]*http.Response
	requests []string
	err      error
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, req.URL.String())
	if f.err != nil {
		return nil, f.err
	}
	resp, ok := f.replies[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return resp, nil
}

func reply(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

const latestURL = "https://api.github.com/repos/olamide226/avar/releases/latest"

// releaseJSON is the shape of GitHub's answer, with the field names and the
// asset names of the real v0.12.12 release.
const releaseJSON = `{
  "tag_name": "v0.12.12",
  "assets": [
    {"name": "avar_0.12.12_darwin_all.tar.gz", "browser_download_url": "https://example.invalid/darwin.tar.gz", "size": 4663142},
    {"name": "avar_0.12.12_windows_amd64.zip", "browser_download_url": "https://example.invalid/win-amd64.zip", "size": 4945564},
    {"name": "checksums.txt", "browser_download_url": "https://example.invalid/checksums.txt", "size": 291}
  ]
}`

func TestLatestRelease_ReadsTheTagAndAssets_REQ_19_4(t *testing.T) {
	client := &fakeHTTP{replies: map[string]*http.Response{latestURL: reply(200, releaseJSON)}}

	release, err := LatestRelease(context.Background(), client, DefaultRepo, "avr/test")
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if release.Tag != "v0.12.12" {
		t.Errorf("tag = %q, want v0.12.12", release.Tag)
	}
	v, err := release.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v.String() != "0.12.12" {
		t.Errorf("version = %s, want 0.12.12", v)
	}
	asset, ok := release.Asset("avar_0.12.12_darwin_all.tar.gz")
	if !ok {
		t.Fatalf("the release's assets do not include the macOS archive: %q", release.AssetNames())
	}
	if asset.URL != "https://example.invalid/darwin.tar.gz" || asset.Size != 4663142 {
		t.Errorf("asset = %+v, want the url and size from the release", asset)
	}
	if _, ok := release.Asset("avar_0.12.12_windows_arm64.zip"); ok {
		t.Error("an asset the release does not publish was reported as present")
	}
}

func TestLatestRelease_RefusesWhatItCannotRead_REQ_19_9(t *testing.T) {
	tests := []struct {
		name     string
		resp     *http.Response
		transErr error
		wantIn   string
	}{
		{name: "no release", resp: reply(404, ""), wantIn: "no release"},
		{name: "rate limited", resp: reply(403, ""), wantIn: "rate limit"},
		{name: "server error", resp: reply(500, ""), wantIn: "500"},
		{name: "not json", resp: reply(200, "<html>"), wantIn: "latest release"},
		{name: "no tag", resp: reply(200, `{"assets":[]}`), wantIn: "no tag"},
		{name: "offline", transErr: errors.New("dial tcp: no route to host"), wantIn: "no route to host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeHTTP{replies: map[string]*http.Response{}, err: tt.transErr}
			if tt.resp != nil {
				client.replies[latestURL] = tt.resp
			}
			_, err := LatestRelease(context.Background(), client, DefaultRepo, "avr/test")
			if err == nil {
				t.Fatalf("LatestRelease accepted a %s reply; nothing should be downloaded after one", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantIn)
			}
		})
	}
}

// Verification is the whole of avar's trust in a download, so each way it can
// fail is a test: altered bytes, a body that stops early, and a checksum that
// is not one.
func TestDownloadVerified_RefusesAnythingButTheRecordedFile_REQ_19_5(t *testing.T) {
	body := []byte("the release archive")
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])
	asset := Asset{Name: "avar_1.0.0_darwin_all.tar.gz", URL: "https://example.invalid/a.tar.gz", Size: int64(len(body))}

	t.Run("the file the release published", func(t *testing.T) {
		var out bytes.Buffer
		client := &fakeHTTP{replies: map[string]*http.Response{asset.URL: reply(200, string(body))}}
		if err := DownloadVerified(context.Background(), client, asset, "avr/test", good, &out); err != nil {
			t.Fatalf("DownloadVerified: %v", err)
		}
		if out.String() != string(body) {
			t.Errorf("wrote %q, want the downloaded bytes", out.String())
		}
	})

	t.Run("altered bytes", func(t *testing.T) {
		var out bytes.Buffer
		altered := "the release archive!"
		client := &fakeHTTP{replies: map[string]*http.Response{asset.URL: reply(200, altered)}}
		altAsset := asset
		altAsset.Size = int64(len(altered))
		err := DownloadVerified(context.Background(), client, altAsset, "avr/test", good, &out)
		if !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("DownloadVerified on altered bytes = %v, want ErrChecksumMismatch", err)
		}
	})

	t.Run("a body that stops early", func(t *testing.T) {
		var out bytes.Buffer
		client := &fakeHTTP{replies: map[string]*http.Response{asset.URL: reply(200, string(body[:5]))}}
		err := DownloadVerified(context.Background(), client, asset, "avr/test", good, &out)
		if !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("DownloadVerified on a truncated body = %v, want ErrChecksumMismatch", err)
		}
		if !strings.Contains(err.Error(), "ended after 5 of 19 bytes") {
			t.Errorf("error = %v, want it to say how much arrived", err)
		}
	})

	t.Run("a checksum that is not one", func(t *testing.T) {
		var out bytes.Buffer
		client := &fakeHTTP{replies: map[string]*http.Response{asset.URL: reply(200, string(body))}}
		err := DownloadVerified(context.Background(), client, asset, "avr/test", "not-a-checksum", &out)
		if !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("DownloadVerified with a malformed checksum = %v, want ErrChecksumMismatch", err)
		}
		if len(client.requests) != 0 {
			t.Errorf("it made %d requests for a download it could never verify", len(client.requests))
		}
	})
}

func TestFetchChecksums_ReadsTheReleasesOwnFile_REQ_19_5(t *testing.T) {
	asset := Asset{Name: ChecksumsAsset, URL: "https://example.invalid/checksums.txt"}
	client := &fakeHTTP{replies: map[string]*http.Response{
		asset.URL: reply(200, "f5e8b9f794e437a8bad41a42034685ff1415ae81291b9676fbdbd148f54a3076  avar_0.12.12_darwin_all.tar.gz\n"),
	}}

	sums, err := FetchChecksums(context.Background(), client, asset, "avr/test")
	if err != nil {
		t.Fatalf("FetchChecksums: %v", err)
	}
	if got := sums["avar_0.12.12_darwin_all.tar.gz"]; got != "f5e8b9f794e437a8bad41a42034685ff1415ae81291b9676fbdbd148f54a3076" {
		t.Errorf("checksum = %q, want the one in the file", got)
	}
}
