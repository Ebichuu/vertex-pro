package torrentdownload

import (
	"context"
	"errors"
	"fmt"
	"github.com/vertex-pro/vertex-pro/internal/sitecredentials"
	"io"
	"net/http"
	"net/url"

	"github.com/vertex-pro/vertex-pro/internal/outbound"
	"strings"
	"time"
)

const DefaultMaxArtifactBytes int64 = 16 << 20

var allowedContentTypes = map[string]struct{}{
	"application/x-bittorrent": {},
	"application/octet-stream": {},
	"application/x-torrent":    {},
}

type Config struct {
	Client       *http.Client
	AllowedHosts []string
	// RejectPrivateIPs 在启用时拒绝解析到私网/环回/链路本地的目标（HARD-04
	// DNS 绕过防线）；默认关闭以保持本地开发与测试行为。
	RejectPrivateIPs bool
	Resolver         outbound.Resolver
	MaxBytes         int64
	Timeout          time.Duration
	UserAgent        string
}

type Request struct {
	CredentialRef    string
	CredentialOrigin string
	URL              string
}

type Artifact struct {
	URL         string
	StatusCode  int
	Body        []byte
	ContentType string
}

type Downloader struct {
	client          *http.Client
	allowedHosts    map[string]struct{}
	rejectPrivateIP bool
	resolver        outbound.Resolver
	maxBytes        int64
	userAgent       string
}

func New(config Config) (*Downloader, error) {
	if config.MaxBytes <= 0 {
		config.MaxBytes = DefaultMaxArtifactBytes
	}
	if config.Timeout <= 0 {
		config.Timeout = 20 * time.Second
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: config.Timeout}
	}
	hosts := make(map[string]struct{}, len(config.AllowedHosts))
	for _, host := range config.AllowedHosts {
		host = normalizeHost(host)
		if host != "" {
			hosts[host] = struct{}{}
		}
	}
	if len(hosts) == 0 {
		return nil, errors.New("at least one allowed host is required")
	}
	client := *config.Client
	previousRedirect := client.CheckRedirect
	resolver := config.Resolver
	if resolver == nil {
		resolver = outbound.DefaultResolver{}
	}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many torrent redirects")
		}
		parsed, err := parseAllowedURL(request.URL.String(), hosts)
		if err != nil {
			return err
		}
		if config.RejectPrivateIPs {
			if err := outbound.ValidateHost(request.Context(), resolver, parsed.Host); err != nil {
				return fmt.Errorf("redirect target rejected: %w", err)
			}
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		return nil
	}
	userAgent := strings.TrimSpace(config.UserAgent)
	if userAgent == "" {
		userAgent = "vertex-pro/torrent-downloader"
	}
	return &Downloader{client: &client, allowedHosts: hosts, maxBytes: config.MaxBytes, userAgent: userAgent, rejectPrivateIP: config.RejectPrivateIPs, resolver: resolver}, nil
}

func (downloader *Downloader) Download(ctx context.Context, input Request) (Artifact, error) {
	if downloader == nil || downloader.client == nil || downloader.maxBytes <= 0 {
		return Artifact{}, errors.New("downloader is not initialized")
	}
	parsed, err := parseAllowedURL(input.URL, downloader.allowedHosts)
	if err != nil {
		return Artifact{}, err
	}
	if downloader.rejectPrivateIP {
		if err := outbound.ValidateHost(ctx, downloader.resolver, parsed.Host); err != nil {
			return Artifact{}, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Artifact{}, fmt.Errorf("create artifact request: %w", err)
	}
	request.Header.Set("Accept", "application/x-bittorrent, application/octet-stream;q=0.9")
	request.Header.Set("User-Agent", downloader.userAgent)
	credentialClient, err := sitecredentials.Apply(request, downloader.client, input.CredentialRef, input.CredentialOrigin)
	if err != nil {
		return Artifact{}, err
	}
	response, err := credentialClient.Do(request)
	if err != nil {
		return Artifact{}, fmt.Errorf("download artifact: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Artifact{}, fmt.Errorf("artifact returned HTTP %d", response.StatusCode)
	}
	contentType := normalizeContentType(response.Header.Get("Content-Type"))
	if _, ok := allowedContentTypes[contentType]; !ok {
		return Artifact{}, fmt.Errorf("unsupported artifact content type %q", contentType)
	}
	if response.ContentLength > downloader.maxBytes {
		return Artifact{}, fmt.Errorf("artifact body exceeds limit: %d bytes", downloader.maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, downloader.maxBytes+1))
	if err != nil {
		return Artifact{}, fmt.Errorf("read artifact body: %w", err)
	}
	if int64(len(body)) > downloader.maxBytes {
		return Artifact{}, fmt.Errorf("artifact body exceeds limit: %d bytes", downloader.maxBytes)
	}
	return Artifact{URL: parsed.String(), StatusCode: response.StatusCode, Body: body, ContentType: contentType}, nil
}

func parseAllowedURL(raw string, allowedHosts map[string]struct{}) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, errors.New("invalid artifact URL")
	}
	if _, ok := allowedHosts[normalizeHost(parsed.Host)]; !ok {
		return nil, fmt.Errorf("artifact host %q is not allowlisted", parsed.Host)
	}
	return parsed, nil
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func normalizeContentType(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return value
}
