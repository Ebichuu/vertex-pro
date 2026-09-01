package qbprofile

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
)

type Proxy interface {
	Version(context.Context) (string, error)
	WebAPIVersion(context.Context) (string, error)
	BuildInfo(context.Context) (map[string]any, error)
	ProcessInfo(context.Context) (map[string]any, error)
}

type Profile struct {
	ID                       string
	QBAppVersion             string
	WebAPIVersion            string
	LibtorrentVersion        string
	AddStartField            string
	AddResponseFormat        string
	IdentityMode             string
	SupportsV2               bool
	SupportsHybrid           bool
	SupportsProcessInfo      bool
	ProfileVerificationState string
}

func Probe(ctx context.Context, client Proxy) (Profile, error) {
	if client == nil {
		return Profile{}, errors.New("proxy client is required")
	}
	appVersion, err := client.Version(ctx)
	if err != nil {
		return Profile{}, fmt.Errorf("probe qB app version: %w", err)
	}
	webAPI, err := client.WebAPIVersion(ctx)
	if err != nil {
		return Profile{}, fmt.Errorf("probe qB WebAPI version: %w", err)
	}
	buildInfo, err := client.BuildInfo(ctx)
	if err != nil {
		return Profile{}, fmt.Errorf("probe qB build info: %w", err)
	}
	libtorrent := stringValue(buildInfo, "libtorrent", "libtorrent_version")
	major, minor, err := versionParts(webAPI)
	if err != nil {
		return Profile{}, fmt.Errorf("parse qB WebAPI version %q: %w", webAPI, err)
	}
	profile := Profile{
		QBAppVersion:             appVersion,
		WebAPIVersion:            webAPI,
		LibtorrentVersion:        libtorrent,
		ProfileVerificationState: "EXPECTED",
	}
	switch {
	case major == 2 && minor <= 8:
		profile.ID = "QB_438"
		profile.AddStartField = "paused"
		profile.AddResponseFormat = "legacy_text"
		profile.IdentityMode = "legacy_v1"
	case major == 2 && minor >= 11 && minor < 14:
		profile.ID = "QB_504"
		profile.AddStartField = "stopped"
		profile.AddResponseFormat = "legacy_text"
		profile.IdentityMode = "dual_hash"
	case major == 2 && minor >= 14:
		profile.ID = "QB_522"
		profile.AddStartField = "stopped"
		profile.AddResponseFormat = "json_counts"
		profile.IdentityMode = "dual_hash"
	default:
		return Profile{}, fmt.Errorf("unsupported qB WebAPI version %q", webAPI)
	}
	libtorrentMajor, _, libtorrentErr := versionParts(libtorrent)

	if libtorrentErr != nil {

		return Profile{}, fmt.Errorf("parse qB libtorrent version %q: %w", libtorrent, libtorrentErr)

	}
	profile.SupportsV2 = libtorrentMajor >= 2
	profile.SupportsHybrid = libtorrentMajor >= 2
	processInfo, processErr := client.ProcessInfo(ctx)
	if processErr == nil && processInfo != nil {
		profile.SupportsProcessInfo = true
	} else if processErr != nil {
		var httpErr *proxy.HTTPError
		if !errors.As(processErr, &httpErr) || httpErr.StatusCode != 404 {
			return Profile{}, fmt.Errorf("probe qB process info: %w", processErr)
		}
	}
	return profile, nil
}

func stringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			return value
		}
	}
	return ""
}

func versionParts(value string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, errors.New("version is incomplete")
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return 0, 0, errors.New("invalid major version")
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return 0, 0, errors.New("invalid minor version")
	}
	return major, minor, nil
}
