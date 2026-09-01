package torrentdownload

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloaderAcceptsAllowlistedTorrentArtifact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Accept"); !strings.Contains(got, "application/x-bittorrent") {
			t.Errorf("Accept=%q", got)
		}
		if got := request.Header.Get("User-Agent"); got == "" {
			t.Error("User-Agent is empty")
		}
		writer.Header().Set("Content-Type", "application/x-bittorrent; charset=binary")
		_, _ = writer.Write([]byte("d4:infod4:name4:testee"))
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	downloader, err := New(Config{Client: server.Client(), AllowedHosts: []string{host}, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := downloader.Download(context.Background(), Request{URL: server.URL + "/test.torrent"})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.StatusCode != http.StatusOK || artifact.ContentType != "application/x-bittorrent" || string(artifact.Body) != "d4:infod4:name4:testee" {
		t.Fatalf("artifact=%+v", artifact)
	}
}

func TestDownloaderRejectsHTMLLoginPageAndNonSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/login" {
			writer.Header().Set("Content-Type", "text/html")
			_, _ = writer.Write([]byte("<html>login</html>"))
			return
		}
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	downloader, err := New(Config{Client: server.Client(), AllowedHosts: []string{host}, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/login", "/forbidden"} {
		if _, err := downloader.Download(context.Background(), Request{URL: server.URL + path}); err == nil {
			t.Fatalf("path %s unexpectedly succeeded", path)
		}
	}
}

func TestDownloaderRejectsHostAndContentTypeAndOversize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("123456"))
	}))
	defer server.Close()
	other := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write([]byte("ok"))
	}))
	defer other.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	downloader, err := New(Config{Client: server.Client(), AllowedHosts: []string{host}, MaxBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := downloader.Download(context.Background(), Request{URL: server.URL}); err == nil {
		t.Fatal("expected content type/size error")
	}
	if _, err := downloader.Download(context.Background(), Request{URL: other.URL}); err == nil {
		t.Fatal("expected host allowlist error")
	}
	if _, err := downloader.Download(context.Background(), Request{URL: "file:///tmp/test.torrent"}); err == nil {
		t.Fatal("expected URL error")
	}
}

func TestDownloaderRejectsRedirectOutsideAllowlist(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write([]byte("secret"))
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	downloader, err := New(Config{Client: server.Client(), AllowedHosts: []string{host}, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := downloader.Download(context.Background(), Request{URL: server.URL}); err == nil {
		t.Fatal("expected redirect rejection")
	}
}

func TestDownloaderPropagatesReadAndContextErrors(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		if hijacker, ok := writer.(http.Hijacker); ok {
			connection, _, err := hijacker.Hijack()
			if err == nil {
				_ = connection.Close()
			}
		}
	}))
	defer broken.Close()
	host := strings.TrimPrefix(broken.URL, "http://")
	downloader, err := New(Config{Client: broken.Client(), AllowedHosts: []string{host}, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := downloader.Download(context.Background(), Request{URL: broken.URL}); err == nil {
		t.Fatal("expected read error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = downloader.Download(ctx, Request{URL: broken.URL})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	_ = io.EOF
}

func TestNewRejectsMissingAllowlist(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected allowlist error")
	}
}
