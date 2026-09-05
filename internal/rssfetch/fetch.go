package rssfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/outbound"
)

const (
	DefaultMaxBodyBytes = 4 << 20
	DefaultTimeout      = 15 * time.Second
)

type Request struct {
	URL          string
	ETag         string
	LastModified string
}

type Response struct {
	StatusCode   int
	Body         []byte
	ETag         string
	LastModified string
	ContentType  string
}

// Boundary 是 HARD-04 出站边界：可选的主机白名单、私网/环回目标拒绝与
// DNS 解析校验。未设置（nil）时保持历史行为，供本地开发与测试使用。
type Boundary struct {
	AllowedHosts     map[string]struct{}
	RejectPrivateIPs bool
	Resolver         outbound.Resolver
}

type Fetcher struct {
	client       *http.Client
	maxBodyBytes int64
	userAgent    string
	boundary     *Boundary
}

// WithBoundary 返回启用出站边界的 Fetcher 副本：每个重定向跳都会用同一
// 边界重新校验（scheme、userinfo、可选白名单、可选私网拒绝）。
func (fetcher Fetcher) WithBoundary(boundary Boundary) Fetcher {
	client := *fetcher.client
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		parsed, err := url.Parse(request.URL.String())
		if err != nil {
			return fmt.Errorf("parse redirect target: %w", err)
		}
		if err := outbound.ValidateURL(request.Context(), parsed, boundary.Resolver, boundary.AllowedHosts, boundary.RejectPrivateIPs); err != nil {
			return fmt.Errorf("redirect target rejected: %w", err)
		}
		return nil
	}
	hardened := boundary
	return Fetcher{client: &client, maxBodyBytes: fetcher.maxBodyBytes, userAgent: fetcher.userAgent, boundary: &hardened}
}

func New(client *http.Client, maxBodyBytes int64) (Fetcher, error) {
	if maxBodyBytes <= 0 {
		return Fetcher{}, errors.New("max body bytes must be positive")
	}
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	return Fetcher{client: client, maxBodyBytes: maxBodyBytes, userAgent: "vertex-pro/rss-fetcher"}, nil
}

func (fetcher Fetcher) Fetch(ctx context.Context, input Request) (Response, error) {
	if fetcher.client == nil {
		return Response{}, errors.New("fetcher is not initialized")
	}
	if fetcher.maxBodyBytes <= 0 {
		return Response{}, errors.New("max body bytes must be positive")
	}
	parsed, err := validateURL(input.URL)
	if err != nil {
		return Response{}, err
	}
	if fetcher.boundary != nil {
		if err := outbound.ValidateURL(ctx, parsed, fetcher.boundary.Resolver, fetcher.boundary.AllowedHosts, fetcher.boundary.RejectPrivateIPs); err != nil {
			return Response{}, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Response{}, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.1")
	if fetcher.userAgent != "" {
		request.Header.Set("User-Agent", fetcher.userAgent)
	}
	if strings.TrimSpace(input.ETag) != "" {
		request.Header.Set("If-None-Match", input.ETag)
	}
	if strings.TrimSpace(input.LastModified) != "" {
		request.Header.Set("If-Modified-Since", input.LastModified)
	}

	response, err := fetcher.client.Do(request)
	if err != nil {
		return Response{}, fmt.Errorf("fetch feed: %w", err)
	}
	defer response.Body.Close()
	result := Response{
		StatusCode:   response.StatusCode,
		ETag:         response.Header.Get("ETag"),
		LastModified: response.Header.Get("Last-Modified"),
		ContentType:  response.Header.Get("Content-Type"),
	}
	if response.StatusCode == http.StatusNotModified {
		return result, nil
	}
	if response.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("feed returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > fetcher.maxBodyBytes {
		return Response{}, fmt.Errorf("feed body exceeds limit: %d bytes", fetcher.maxBodyBytes)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, fetcher.maxBodyBytes+1))
	if err != nil {
		return Response{}, fmt.Errorf("read feed body: %w", err)
	}
	if int64(len(body)) > fetcher.maxBodyBytes {
		return Response{}, fmt.Errorf("feed body exceeds limit: %d bytes", fetcher.maxBodyBytes)
	}
	result.Body = body
	return result, nil
}

func validateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, errors.New("invalid feed URL")
	}
	return parsed, nil
}
