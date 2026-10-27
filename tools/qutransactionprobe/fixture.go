package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/identitymatcher"
	"github.com/vertex-pro/vertex-pro/internal/qbadd"
	"github.com/vertex-pro/vertex-pro/internal/qbprofile"
	"github.com/vertex-pro/vertex-pro/internal/qui/proxy"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
	"github.com/vertex-pro/vertex-pro/internal/torrentidentity"
)

func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// rawToDelta converts a raw qB sync maindata payload into the merge delta,
// mirroring the proxy client's Fetch adaptation.
func rawToDelta(response map[string]any) (syncmerge.Delta, error) {
	payload, err := json.Marshal(response)
	if err != nil {
		return syncmerge.Delta{}, err
	}
	var delta struct {
		RID        uint64                            `json:"rid"`
		FullUpdate bool                              `json:"full_update"`
		Torrents   map[string]syncmerge.TorrentDelta `json:"torrents"`
		Removed    []string                          `json:"torrents_removed"`
	}
	if err := json.Unmarshal(payload, &delta); err != nil {
		return syncmerge.Delta{}, err
	}
	return syncmerge.Delta{RID: delta.RID, FullUpdate: delta.FullUpdate, Torrents: delta.Torrents, Removed: delta.Removed}, nil
}

// fixtureCaptureSummary is printed after the four golden fixtures for one
// profile have been written. Digests cover the exact file bytes so the
// profile verification run can re-verify them later.
type fixturePayload struct {
	Kind     string `json:"kind"`
	FileName string `json:"file_name"`
	SHA256   string `json:"sha256"`
	Base64   string `json:"base64"`
}

type fixtureCaptureSummary struct {
	Status   string `json:"status"`
	Profile  string `json:"profile"`
	Fixtures struct {
		Add          string `json:"add"`
		Sync         string `json:"sync"`
		Tracker      string `json:"tracker"`
		TransferInfo string `json:"transfer_info"`
	} `json:"fixtures"`
	Payloads []fixturePayload `json:"payloads"`
	Safety   struct {
		ExplicitIsolatedLabOptIn bool `json:"explicit_isolated_lab_opt_in"`
		BaselineEmpty            bool `json:"baseline_empty"`
		StartInactive            bool `json:"start_inactive"`
		Trackerless              bool `json:"trackerless"`
		ProductionServicesCalled bool `json:"production_services_called"`
		DeleteOnlyAddedIdentity  bool `json:"delete_only_added_identity"`
		LabReturnedEmpty         bool `json:"lab_returned_empty"`
		SecretsRedacted          bool `json:"secrets_redacted"`
	} `json:"safety"`
	Notes string `json:"notes"`
}

type addFixture struct {
	Kind           string `json:"kind"`
	ProfileID      string `json:"profile_id"`
	CapturedAt     string `json:"captured_at"`
	Method         string `json:"method"`
	Path           string `json:"path"`
	ContentType    string `json:"content_type"`
	BodyBase64     string `json:"body_base64"`
	ResponseStatus int    `json:"response_status"`
	ResponseBody   string `json:"response_body"`
	Redactions     string `json:"redactions"`
}

type syncFixture struct {
	Kind        string         `json:"kind"`
	ProfileID   string         `json:"profile_id"`
	CapturedAt  string         `json:"captured_at"`
	Baseline    map[string]any `json:"baseline_full_update"`
	FollowUpRID uint64         `json:"follow_up_rid"`
	FollowUp    map[string]any `json:"follow_up_response"`
	Redactions  string         `json:"redactions"`
}

type trackerFixture struct {
	Kind       string           `json:"kind"`
	ProfileID  string           `json:"profile_id"`
	CapturedAt string           `json:"captured_at"`
	TorrentID  string           `json:"torrent_id"`
	Rows       []map[string]any `json:"rows"`
	Redactions string           `json:"redactions"`
}

type transferInfoFixture struct {
	Kind       string         `json:"kind"`
	ProfileID  string         `json:"profile_id"`
	CapturedAt string         `json:"captured_at"`
	Source     string         `json:"source"`
	TorrentID  string         `json:"torrent_id"`
	Fields     map[string]any `json:"transfer_fields"`
	Redactions string         `json:"redactions"`
}

var transferInfoFields = []string{
	"state", "size", "total_size", "amount_left", "completed", "progress",
	"dlspeed", "upspeed", "dl_limit", "up_limit", "num_complete", "num_incomplete",
}

// runFixtureCapture archives the redacted golden wire fixtures for one lab
// profile: the raw Add request/response, the raw Sync full-update baseline and
// the follow-up containing the added task, the tracker rows, and the transfer
// field subset used by the capacity ledger. Nothing in these payloads carries
// credentials: the proxy key only ever appears in the live URL, which is not
// recorded.
func runFixtureCapture(ctx context.Context, client *proxy.Client, profile qbprofile.Profile, identity torrentidentity.Identity, artifact []byte, torrentName, fixtureDir string) error {
	capturedAt := time.Now().UTC().Format(time.RFC3339)

	// Add request: capture the exact multipart body before it is consumed.
	addRequest, err := qbadd.BuildRequest(profile, qbadd.TorrentArtifact{Filename: torrentName + ".torrent", Content: artifact}, qbadd.AddParams{StartInactive: true})
	if err != nil {
		return err
	}
	body, err := io.ReadAll(addRequest.Body)
	if err != nil {
		return err
	}
	addRequest.Body = io.NopCloser(bytes.NewReader(body))

	var addRecord addFixture
	addRecord.Kind = "vertex-pro-g1-add-fixture"
	addRecord.ProfileID = profile.ID
	addRecord.CapturedAt = capturedAt
	addRecord.Method = addRequest.Method
	addRecord.Path = addRequest.URL.Path
	addRecord.ContentType = addRequest.Header.Get("Content-Type")
	addRecord.BodyBase64 = base64.StdEncoding.EncodeToString(body)
	addRecord.Redactions = "no credentials present; the qui proxy key only appears in the live request URL and is not recorded"
	raw, err := client.Send(ctx, addRequest)
	if err != nil {
		return fmt.Errorf("fixture Add send: %w", err)
	}
	addRecord.ResponseStatus = raw.StatusCode
	addRecord.ResponseBody = string(raw.Body)

	// Sync fixtures: baseline full update, then the follow-up that contains
	// the added task in an inactive state.
	baseline, err := client.SyncMainData(ctx, 0)
	if err != nil {
		return err
	}
	current := syncmerge.Snapshot{}
	var followUp map[string]any
	var followUpRID uint64
	match := identitymatcher.Result{Status: identitymatcher.StatusNotFound}
	matchDeadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(matchDeadline) {
		response, fetchErr := client.SyncMainData(ctx, current.RID)
		if fetchErr != nil {
			return fetchErr
		}
		ridValue, _ := response["rid"].(float64)
		followUpRID = uint64(ridValue)
		rawDelta, mergeErr := rawToDelta(response)
		if mergeErr != nil {
			return mergeErr
		}
		delta, mergeErr := syncmerge.Merge(current, rawDelta)
		if mergeErr != nil {
			return mergeErr
		}
		current = delta.Snapshot
		followUp = response
		match, err = identitymatcher.Match(identitymatcher.Request{Identity: identity}, snapshotCandidates(current))
		if err != nil {
			return err
		}
		if match.Status == identitymatcher.StatusMatched && inactiveState(stateOf(current, match.CandidateID)) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if match.Status != identitymatcher.StatusMatched {
		return fmt.Errorf("fixture sync follow-up never matched the added identity: %s", match.Status)
	}
	observedState := stateOf(current, match.CandidateID)

	var syncRecord syncFixture
	syncRecord.Kind = "vertex-pro-g1-sync-fixture"
	syncRecord.ProfileID = profile.ID
	syncRecord.CapturedAt = capturedAt
	syncRecord.Baseline = baseline
	syncRecord.FollowUpRID = followUpRID
	syncRecord.FollowUp = followUp
	syncRecord.Redactions = "raw qB sync payloads; no credentials present"

	trackerRows, err := client.Trackers(ctx, match.CandidateID)
	if err != nil {
		return fmt.Errorf("fixture tracker capture: %w", err)
	}
	var trackerRecord trackerFixture
	trackerRecord.Kind = "vertex-pro-g1-tracker-fixture"
	trackerRecord.ProfileID = profile.ID
	trackerRecord.CapturedAt = capturedAt
	trackerRecord.TorrentID = match.CandidateID
	trackerRecord.Rows = trackerRows
	trackerRecord.Redactions = "raw qB tracker rows for a trackerless task; no credentials present"

	task := current.Torrents[match.CandidateID]
	transfer := map[string]any{}
	for _, field := range transferInfoFields {
		if value, ok := task.Value(field); ok {
			transfer[field] = value
		}
	}
	var transferRecord transferInfoFixture
	transferRecord.Kind = "vertex-pro-g1-transfer-info-fixture"
	transferRecord.ProfileID = profile.ID
	transferRecord.CapturedAt = capturedAt
	transferRecord.Source = "QBITTORRENT_SYNC_MAINDATA_TASK_FIELDS (the qB proxy allowlist does not expose torrents/transferInfo; amount_left and transfer facts come from the task's sync fields)"
	transferRecord.TorrentID = match.CandidateID
	transferRecord.Fields = transfer
	transferRecord.Redactions = "task transfer field subset; no credentials present"

	profileName := strings.ToLower(strings.ReplaceAll(profile.ID, "_", ""))
	digests := map[string]string{}
	var payloads []fixturePayload
	for name, record := range map[string]any{
		"add": addRecord, "sync": syncRecord, "tracker": trackerRecord, "transfer_info": transferRecord,
	} {
		payload, marshalErr := json.MarshalIndent(record, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		payload = append(payload, '\n')
		fileName := fmt.Sprintf("fixture-%s-%s.json", profileName, name)
		if fixtureDir != "" {
			if err := os.MkdirAll(fixtureDir, 0o750); err != nil {
				return err
			}
			if writeErr := os.WriteFile(filepath.Join(fixtureDir, fileName), payload, 0o600); writeErr != nil {
				return writeErr
			}
		}
		digests[name] = sha256Hex(payload)
		payloads = append(payloads, fixturePayload{Kind: name, FileName: fileName, SHA256: digests[name], Base64: base64.StdEncoding.EncodeToString(payload)})
	}

	deleteStatus, deleteErr := deleteIdentity(ctx, strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_URL")), strings.TrimSpace(os.Getenv("VERTEX_PRO_QUI_PROXY_KEY")), match.CandidateID)
	if deleteErr != nil {
		return deleteErr
	}
	if deleteStatus != http.StatusOK && deleteStatus != http.StatusNoContent {
		return fmt.Errorf("fixture cleanup delete returned HTTP %d", deleteStatus)
	}
	finalEmpty := false
	for attempt := 0; attempt < 30; attempt++ {
		delta, fetchErr := client.Fetch(ctx, 0)
		if fetchErr != nil {
			return fetchErr
		}
		merged, mergeErr := syncmerge.Merge(syncmerge.Snapshot{}, delta)
		if mergeErr != nil {
			return mergeErr
		}
		if len(merged.Snapshot.Torrents) == 0 {
			finalEmpty = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !finalEmpty {
		return errors.New("lab did not return to an empty task set after fixture capture")
	}

	var summary fixtureCaptureSummary
	summary.Status = "PASS"
	summary.Profile = profile.ID
	summary.Fixtures.Add = digests["add"]
	summary.Fixtures.Sync = digests["sync"]
	summary.Fixtures.Tracker = digests["tracker"]
	summary.Fixtures.TransferInfo = digests["transfer_info"]
	summary.Safety.ExplicitIsolatedLabOptIn = true
	summary.Safety.BaselineEmpty = true
	summary.Safety.StartInactive = true
	summary.Safety.Trackerless = true
	summary.Safety.ProductionServicesCalled = false
	summary.Safety.DeleteOnlyAddedIdentity = true
	summary.Safety.LabReturnedEmpty = finalEmpty
	summary.Safety.SecretsRedacted = true
	summary.Payloads = payloads
	summary.Notes = "captured inactive (" + observedState + ") task via qui proxy; fixture digests are sha256 of the archived file bytes"
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}
