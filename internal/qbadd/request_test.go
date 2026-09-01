package qbadd

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
)

func TestBuildRequestUsesProfileStartFieldAndSingleTorrent(t *testing.T) {
	cases := []struct{ name, start string }{{name: "QB_438", start: "paused"}, {name: "QB_504", start: "stopped"}, {name: "QB_522", start: "stopped"}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request, err := BuildRequest(qbprofile.Profile{ID: testCase.name, AddStartField: testCase.start}, TorrentArtifact{Filename: "race.torrent", Content: []byte("torrent")}, AddParams{SavePath: "/data/race", Category: "vertex-pro", Tags: []string{"vertex-pro", "free"}})
			if err != nil {
				t.Fatalf("BuildRequest() error = %v", err)
			}
			if request.Method != http.MethodPost || request.URL.Path != "/api/v2/torrents/add" {
				t.Fatalf("request = %s %s", request.Method, request.URL)
			}
			mediaType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
			if err != nil || mediaType != "multipart/form-data" {
				t.Fatalf("content type = %q, %v", request.Header.Get("Content-Type"), err)
			}
			reader := multipart.NewReader(request.Body, params["boundary"])
			fields := map[string][]string{}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("read multipart: %v", err)
				}
				data, _ := io.ReadAll(part)
				fields[part.FormName()] = append(fields[part.FormName()], string(data))
				if part.FormName() == "torrents" && part.FileName() != "race.torrent" {
					t.Fatalf("torrent filename = %q", part.FileName())
				}
			}
			if len(fields["torrents"]) != 1 || fields["torrents"][0] != "torrent" {
				t.Fatalf("torrent fields = %+v", fields["torrents"])
			}
			if fields[testCase.start][0] != "false" || fields["autoTMM"][0] != "false" || fields["tags"][0] != "vertex-pro,free" {
				t.Fatalf("fields = %+v", fields)
			}
		})
	}
}

func TestBuildRequestCanCreateAnExplicitlyInactiveTestTask(t *testing.T) {
	request, err := BuildRequest(qbprofile.Profile{ID: "QB_504", AddStartField: "stopped"}, TorrentArtifact{Filename: "probe.torrent", Content: []byte("torrent")}, AddParams{StartInactive: true})
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(request.Body, params["boundary"])
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if part.FormName() == "stopped" {
			value, readErr := io.ReadAll(part)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(value) != "true" {
				t.Fatalf("stopped = %q", value)
			}
			return
		}
	}
	t.Fatal("stopped field is missing")
}

func TestBuildRequestRejectsBatchAndUnsafeValues(t *testing.T) {
	profile := qbprofile.Profile{AddStartField: "stopped"}
	cases := []TorrentArtifact{{Filename: "", Content: []byte("x")}, {Filename: "../secret.torrent", Content: []byte("x")}, {Filename: "x.torrent", Content: nil}}
	for _, artifact := range cases {
		if _, err := BuildRequest(profile, artifact, AddParams{}); err == nil {
			t.Fatalf("artifact %+v error = nil", artifact)
		}
	}
	if _, err := BuildRequest(profile, TorrentArtifact{Filename: "x.torrent", Content: []byte("x")}, AddParams{Tags: []string{"safe,bad"}}); err == nil {
		t.Fatal("unsafe tag error = nil")
	}
	if _, err := BuildRequest(profile, TorrentArtifact{Filename: "x.torrent", Content: []byte("x")}, AddParams{SavePath: "/data\r\nX"}); err == nil {
		t.Fatal("unsafe savepath error = nil")
	}
}

func TestBuildRequestDoesNotSupportURLAddOrUnknownFields(t *testing.T) {
	request, err := BuildRequest(qbprofile.Profile{AddStartField: "paused"}, TorrentArtifact{Filename: "x.torrent", Content: []byte("x")}, AddParams{})
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	if request.URL.Host != "" || strings.Contains(request.URL.String(), "http") {
		t.Fatalf("request unexpectedly has remote URL: %s", request.URL)
	}
	if IsAddEndpoint(request.URL) == false {
		t.Fatal("add endpoint marker = false")
	}
}

func TestBuildRequestRejectsUnknownProfileStartField(t *testing.T) {
	if _, err := BuildRequest(qbprofile.Profile{AddStartField: "running"}, TorrentArtifact{Filename: "x.torrent", Content: []byte("x")}, AddParams{}); err == nil {
		t.Fatal("unknown profile field error = nil")
	}
}
