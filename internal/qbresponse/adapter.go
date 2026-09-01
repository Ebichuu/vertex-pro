package qbresponse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type RawResponse struct {
	APIVersion   string
	StatusCode   int
	Body         []byte
	TransportErr error
}

type Result struct {
	APIVersion      string
	ResponseFormat  string
	HTTPStatus      int
	SuccessCount    int
	PendingCount    int
	FailureCount    int
	AddedTorrentIDs []string
	DispatchState   string
	SideEffectFact  string
	RawBodyDigest   string
}

func ClassifyNotSent(apiVersion string, err error) (Result, error) {
	if err == nil {
		return Result{}, errors.New("not-sent classification requires an error")
	}
	result := baseResult(apiVersion, nil)
	result.ResponseFormat = "transport_error"
	result.DispatchState = "NOT_STARTED"
	result.SideEffectFact = "FAILED_NO_EFFECT"
	return result, nil
}

func Adapt(raw RawResponse) (Result, error) {
	if strings.TrimSpace(raw.APIVersion) == "" {
		return Result{}, errors.New("api version is required")
	}
	result := baseResult(raw.APIVersion, raw.Body)
	result.HTTPStatus = raw.StatusCode
	result.DispatchState = "STARTED"
	if raw.TransportErr != nil {
		result.ResponseFormat = "transport_error"
		result.SideEffectFact = "POSSIBLE_EFFECT"
		return result, nil
	}
	if raw.StatusCode < 200 || raw.StatusCode > 299 && raw.StatusCode != http.StatusConflict {
		result.ResponseFormat = "http_error"
		result.SideEffectFact = "POSSIBLE_EFFECT"
		return result, nil
	}
	if raw.StatusCode == http.StatusNoContent {
		result.ResponseFormat = "empty"
		result.SideEffectFact = "POSSIBLE_EFFECT"
		return result, nil
	}
	body := strings.TrimSpace(string(raw.Body))
	if body == "Ok." || body == "Fails." {
		result.ResponseFormat = "legacy_text"
		result.SideEffectFact = "POSSIBLE_EFFECT"
		if body == "Ok." {
			result.SuccessCount = 1
		} else {
			result.FailureCount = 1
		}
		return result, nil
	}
	var payload struct {
		SuccessCount int      `json:"success_count"`
		PendingCount int      `json:"pending_count"`
		FailureCount int      `json:"failure_count"`
		AddedIDs     []string `json:"added_torrent_ids"`
		Torrents     []string `json:"torrents"`
	}
	if err := json.Unmarshal(raw.Body, &payload); err != nil {
		return Result{}, fmt.Errorf("decode add response: %w", err)
	}
	result.ResponseFormat = "json"
	result.SuccessCount = nonNegative(payload.SuccessCount)
	result.PendingCount = nonNegative(payload.PendingCount)
	result.FailureCount = nonNegative(payload.FailureCount)
	result.AddedTorrentIDs = append(payload.AddedIDs, payload.Torrents...)
	result.SideEffectFact = "POSSIBLE_EFFECT"
	return result, nil
}

func baseResult(apiVersion string, body []byte) Result {
	digest := sha256.Sum256(body)
	return Result{APIVersion: apiVersion, RawBodyDigest: hex.EncodeToString(digest[:])}
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
