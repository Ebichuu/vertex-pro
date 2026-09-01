package qbresponse

import (
	"errors"
	"strings"
	"testing"
)

func TestAdaptLegacyAndJSONResponsesNeverConfirmsHashSeen(t *testing.T) {
	cases := []struct {
		name                      string
		raw                       RawResponse
		format                    string
		success, pending, failure int
	}{
		{name: "legacy ok", raw: RawResponse{APIVersion: "2.8.2", StatusCode: 200, Body: []byte("Ok.")}, format: "legacy_text", success: 1},
		{name: "legacy fails", raw: RawResponse{APIVersion: "2.11.2", StatusCode: 200, Body: []byte("Fails.")}, format: "legacy_text", failure: 1},
		{name: "json success", raw: RawResponse{APIVersion: "2.15.1", StatusCode: 200, Body: []byte(`{"success_count":1,"added_torrent_ids":["hash-1"]}`)}, format: "json", success: 1},
		{name: "json pending", raw: RawResponse{APIVersion: "2.15.1", StatusCode: 202, Body: []byte(`{"pending_count":1}`)}, format: "json", pending: 1},
		{name: "empty 204", raw: RawResponse{APIVersion: "2.15.1", StatusCode: 204}, format: "empty"},
		{name: "conflict", raw: RawResponse{APIVersion: "2.15.1", StatusCode: 409, Body: []byte(`{"failure_count":1}`)}, format: "json", failure: 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := Adapt(testCase.raw)
			if err != nil {
				t.Fatalf("Adapt() error = %v", err)
			}
			if result.ResponseFormat != testCase.format || result.SuccessCount != testCase.success || result.PendingCount != testCase.pending || result.FailureCount != testCase.failure {
				t.Fatalf("result = %+v", result)
			}
			if result.DispatchState != "STARTED" || result.SideEffectFact != "POSSIBLE_EFFECT" {
				t.Fatalf("fact = %+v", result)
			}
			if strings.Contains(result.SideEffectFact, "HASH_SEEN") {
				t.Fatal("response directly confirmed HASH_SEEN")
			}
			if testCase.name == "json success" && len(result.AddedTorrentIDs) != 1 {
				t.Fatalf("added IDs = %+v", result.AddedTorrentIDs)
			}
			if result.RawBodyDigest == "" {
				t.Fatal("raw digest is empty")
			}
		})
	}
}

func TestAdaptTransportAndNotSentFacts(t *testing.T) {
	transport, err := Adapt(RawResponse{APIVersion: "2.15.1", StatusCode: 0, TransportErr: errors.New("timeout")})
	if err != nil || transport.SideEffectFact != "POSSIBLE_EFFECT" || transport.DispatchState != "STARTED" {
		t.Fatalf("transport = %+v, %v", transport, err)
	}
	notSent, err := ClassifyNotSent("2.15.1", errors.New("request build failed"))
	if err != nil || notSent.SideEffectFact != "FAILED_NO_EFFECT" || notSent.DispatchState != "NOT_STARTED" {
		t.Fatalf("not sent = %+v, %v", notSent, err)
	}
}

func TestAdaptRejectsMissingVersionAndMalformedJSON(t *testing.T) {
	if _, err := Adapt(RawResponse{StatusCode: 200, Body: []byte("Ok.")}); err == nil {
		t.Fatal("missing version error = nil")
	}
	if _, err := Adapt(RawResponse{APIVersion: "2.15.1", StatusCode: 200, Body: []byte("not-json")}); err == nil {
		t.Fatal("malformed JSON error = nil")
	}
	if _, err := ClassifyNotSent("2.15.1", nil); err == nil {
		t.Fatal("nil not-sent error = nil")
	}
}
