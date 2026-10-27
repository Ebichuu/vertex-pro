package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// These shapes follow qui bfe94b5e4d63f213af747fab39719d47aae99d22:
// internal/services/automations/service.go (PreviewResult, PreviewTorrent),
// internal/api/handlers/automations.go (AutomationPayload, AutomationDryRunResult),
// internal/models/automation_activity.go (AutomationActivity).
//
// Vertex deliberately has no client for POST .../automations/apply (forced
// execution): every call here is either read-only or a qui dry-run.

// Preview views: "needed" lists what the rule would actually delete (for a
// FREE_SPACE rule, only until the target is met); "eligible" lists every
// torrent matching the condition.
const (
	PreviewViewNeeded   = "needed"
	PreviewViewEligible = "eligible"
)

// PreviewResult lists torrents a delete rule would act on, without acting.
type PreviewResult struct {
	TotalMatches int              `json:"totalMatches"`
	Examples     []PreviewTorrent `json:"examples"`
}

type PreviewTorrent struct {
	Name               string   `json:"name"`
	Hash               string   `json:"hash"`
	Size               int64    `json:"size"`
	TotalSize          int64    `json:"totalSize"`
	Ratio              float64  `json:"ratio"`
	SeedingTime        int64    `json:"seedingTime"`
	Tracker            string   `json:"tracker"`
	Category           string   `json:"category"`
	Tags               string   `json:"tags"`
	State              string   `json:"state"`
	AddedOn            int64    `json:"addedOn"`
	CompletionOn       int64    `json:"completionOn"`
	LastActivity       int64    `json:"lastActivity"`
	Uploaded           int64    `json:"uploaded"`
	Downloaded         int64    `json:"downloaded"`
	Progress           float64  `json:"progress"`
	NumSeeds           int64    `json:"numSeeds"`
	NumComplete        int64    `json:"numComplete"`
	NumLeechs          int64    `json:"numLeechs"`
	NumIncomplete      int64    `json:"numIncomplete"`
	IsUnregistered     bool     `json:"isUnregistered,omitempty"`
	IsCrossSeed        bool     `json:"isCrossSeed,omitempty"`
	IsHardlinkCopy     bool     `json:"isHardlinkCopy,omitempty"`
	HardlinkScope      string   `json:"hardlinkScope,omitempty"`
	HardlinkCrossScope string   `json:"hardlinkCrossScope,omitempty"`
	Score              *float64 `json:"score,omitempty"`
}

// Activity action and outcome values written by qui.
const (
	ActivityDeletedRatio        = "deleted_ratio"
	ActivityDeletedSeeding      = "deleted_seeding"
	ActivityDeletedUnregistered = "deleted_unregistered"
	ActivityDeletedCondition    = "deleted_condition"
	ActivityDeleteFailed        = "delete_failed"
	ActivityDryRunNoMatch       = "dry_run_no_match"

	OutcomeSuccess = "success"
	OutcomeFailed  = "failed"
	OutcomeDryRun  = "dry-run"
)

// Activity is one entry of qui's persisted automation activity log.
type Activity struct {
	ID            int             `json:"id"`
	InstanceID    int             `json:"instanceId"`
	Hash          string          `json:"hash"`
	TorrentName   string          `json:"torrentName,omitempty"`
	TrackerDomain string          `json:"trackerDomain,omitempty"`
	Action        string          `json:"action"`
	RuleID        *int            `json:"ruleId,omitempty"`
	RuleName      string          `json:"ruleName,omitempty"`
	Outcome       string          `json:"outcome"`
	Reason        string          `json:"reason,omitempty"`
	Details       json.RawMessage `json:"details,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// IsDryRun reports a simulated action: qui records dry-runs in the same log.
func (activity Activity) IsDryRun() bool { return activity.Outcome == OutcomeDryRun }

// IsDeletion reports a delete attempt (successful, failed or simulated).
func (activity Activity) IsDeletion() bool {
	return strings.HasPrefix(activity.Action, "deleted_") || activity.Action == ActivityDeleteFailed
}

// DryRunResult is qui's report of a dry-run: the activities it recorded.
type DryRunResult struct {
	Status      string     `json:"status"`
	ActivityIDs []int      `json:"activityIds,omitempty"`
	Activities  []Activity `json:"activities,omitempty"`
}

type previewPayload struct {
	Automation
	PreviewLimit  *int   `json:"previewLimit,omitempty"`
	PreviewOffset *int   `json:"previewOffset,omitempty"`
	PreviewView   string `json:"previewView,omitempty"`
}

// MaxActivityLimit is qui's server-side cap for one activity listing.
const MaxActivityLimit = 1000

// PreviewDelete asks qui which torrents a delete rule would remove. It never
// executes the rule. limit <= 0 lets qui choose its default page size.
func (c *Client) PreviewDelete(ctx context.Context, instance string, automation Automation, limit, offset int, view string) (PreviewResult, error) {
	if err := validateDeleteRule(instance, automation); err != nil {
		return PreviewResult{}, err
	}
	if view != "" && view != PreviewViewNeeded && view != PreviewViewEligible {
		return PreviewResult{}, fmt.Errorf("invalid preview view %q", view)
	}
	if limit < 0 || offset < 0 {
		return PreviewResult{}, errors.New("preview limit and offset must not be negative")
	}
	payload := previewPayload{Automation: automation, PreviewView: view}
	if limit > 0 {
		payload.PreviewLimit = &limit
	}
	if offset > 0 {
		payload.PreviewOffset = &offset
	}
	var result PreviewResult
	if err := c.doJSON(ctx, http.MethodPost, automationsPath(instance)+"/preview", payload, &result); err != nil {
		return PreviewResult{}, err
	}
	if result.TotalMatches < 0 || len(result.Examples) > result.TotalMatches {
		return PreviewResult{}, errors.New("qui preview response is inconsistent")
	}
	for _, torrent := range result.Examples {
		if strings.TrimSpace(torrent.Hash) == "" {
			return PreviewResult{}, errors.New("qui preview response has a torrent without hash")
		}
	}
	return result, nil
}

// DryRun runs a rule once in qui's dry-run mode. qui records what it would do
// in its activity log (outcome "dry-run") and changes no torrent. The rule is
// not saved; DryRun is forced on regardless of the caller's value.
func (c *Client) DryRun(ctx context.Context, instance string, automation Automation) (DryRunResult, error) {
	if err := validateDeleteRule(instance, automation); err != nil {
		return DryRunResult{}, err
	}
	automation.ID = 0
	automation.DryRun = true
	automation.Enabled = true
	var result DryRunResult
	if err := c.doJSON(ctx, http.MethodPost, automationsPath(instance)+"/dry-run", automation, &result); err != nil {
		return DryRunResult{}, err
	}
	for _, activity := range result.Activities {
		if !activity.IsDryRun() {
			return DryRunResult{}, fmt.Errorf("qui dry-run reported a non-dry-run outcome %q", activity.Outcome)
		}
	}
	return result, nil
}

// Activity lists the newest qui activity entries for an instance (qui caps
// limit at MaxActivityLimit; there is no cursor). Entries are validated so an
// audit importer can key them by ID.
func (c *Client) Activity(ctx context.Context, instance string, limit int) ([]Activity, error) {
	if strings.TrimSpace(instance) == "" {
		return nil, errors.New("instance ID is required")
	}
	if limit <= 0 || limit > MaxActivityLimit {
		return nil, fmt.Errorf("activity limit must be between 1 and %d", MaxActivityLimit)
	}
	var activities []Activity
	if err := c.getJSON(ctx, automationsPath(instance)+"/activity?limit="+strconv.Itoa(limit), &activities); err != nil {
		return nil, err
	}
	if activities == nil {
		return nil, errors.New("qui activity response is missing")
	}
	for _, activity := range activities {
		if activity.ID <= 0 || activity.Action == "" || activity.Outcome == "" || activity.CreatedAt.IsZero() {
			return nil, errors.New("qui activity response has an incomplete entry")
		}
	}
	return activities, nil
}

func automationsPath(instance string) string {
	return "/api/instances/" + url.PathEscape(instance) + "/automations"
}

// validateDeleteRule mirrors the qui preconditions that matter for delete
// previews, so callers get a precise local error instead of a generic 400.
func validateDeleteRule(instance string, automation Automation) error {
	if strings.TrimSpace(instance) == "" {
		return errors.New("instance ID is required")
	}
	if strings.TrimSpace(automation.Name) == "" {
		return errors.New("automation name is required")
	}
	deleteAction, _ := automation.Conditions["delete"].(map[string]any)
	if enabled, _ := deleteAction["enabled"].(bool); !enabled {
		return errors.New("automation has no enabled delete action")
	}
	if category, _ := automation.Conditions["category"].(map[string]any); category != nil {
		if enabled, _ := category["enabled"].(bool); enabled {
			return errors.New("qui cannot preview delete and category actions together")
		}
	}
	return nil
}
