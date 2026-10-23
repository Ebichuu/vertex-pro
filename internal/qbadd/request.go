package qbadd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
)

type TorrentArtifact struct {
	Filename string
	Content  []byte
}

type AddParams struct {
	SavePath           string
	Category           string
	Tags               []string
	StartInactive      bool
	SequentialDownload bool
	UploadLimitBytes   int64
}

func BuildRequest(profile qbprofile.Profile, artifact TorrentArtifact, params AddParams) (*http.Request, error) {
	if profile.AddStartField != "paused" && profile.AddStartField != "stopped" {
		return nil, errors.New("profile has unsupported add start field")
	}
	if strings.TrimSpace(artifact.Filename) == "" || filepath.Base(artifact.Filename) != artifact.Filename || strings.ContainsAny(artifact.Filename, "\r\n\x00") {
		return nil, errors.New("torrent filename is invalid")
	}
	if len(artifact.Content) == 0 {
		return nil, errors.New("torrent content is empty")
	}
	if err := validateValue(params.SavePath, "savepath"); err != nil {
		return nil, err
	}
	if err := validateValue(params.Category, "category"); err != nil {
		return nil, err
	}
	tags, err := normalizeTags(params.Tags)
	if err != nil {
		return nil, err
	}

	if params.UploadLimitBytes < 0 {
		return nil, errors.New("invalid upload limit")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("torrents", artifact.Filename)
	if err != nil {
		return nil, fmt.Errorf("create torrent form field: %w", err)
	}
	if _, err := file.Write(artifact.Content); err != nil {
		return nil, fmt.Errorf("write torrent form field: %w", err)
	}
	startValue := "false"
	if params.StartInactive {
		startValue = "true"
	}
	fields := map[string]string{
		"autoTMM":             "false",
		profile.AddStartField: startValue,
	}
	if params.SequentialDownload {
		fields["sequentialDownload"] = "true"
	}
	if params.UploadLimitBytes > 0 {
		fields["upLimit"] = strconv.FormatInt(params.UploadLimitBytes, 10)
	}
	if params.SavePath != "" {
		fields["savepath"] = params.SavePath
	}
	if params.Category != "" {
		fields["category"] = params.Category
	}
	if tags != "" {
		fields["tags"] = tags
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, fmt.Errorf("write %s form field: %w", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close add form: %w", err)
	}
	request, err := http.NewRequest(http.MethodPost, "/api/v2/torrents/add", io.NopCloser(bytes.NewReader(body.Bytes())))
	if err != nil {
		return nil, fmt.Errorf("build add request: %w", err)
	}
	request.ContentLength = int64(body.Len())
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request, nil
}

func validateValue(value, field string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s contains forbidden control characters", field)
	}
	return nil
}

func normalizeTags(tags []string) (string, error) {
	normalized := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if strings.ContainsAny(tag, ",\r\n\x00") {
			return "", errors.New("tag contains forbidden characters")
		}
		normalized = append(normalized, tag)
	}
	return strings.Join(normalized, ","), nil
}

func IsAddEndpoint(requestURL *url.URL) bool {
	return requestURL != nil && requestURL.Path == "/api/v2/torrents/add"
}
