package deletionpolicy

import (
	"encoding/json"
	"time"
)

// The JSON forms persisted by Vertex and exchanged over its API. Durations
// are whole seconds; conversion rejects values a time.Duration cannot hold.

type ThresholdSpec struct {
	Metric  Metric     `json:"metric"`
	Op      Comparison `json:"op,omitempty"`
	Value   float64    `json:"value,omitempty"`
	Seconds int64      `json:"seconds,omitempty"`
}

type TriggerSpec struct {
	All []ThresholdSpec `json:"all"`
}

type WeightSpec struct {
	Metric     Metric  `json:"metric"`
	Multiplier float64 `json:"multiplier"`
}

type WatermarkSpec struct {
	MinFreeBytes  int64              `json:"min_free_bytes"`
	FreeSpacePath string             `json:"free_space_path,omitempty"`
	Eligible      []ThresholdSpec    `json:"eligible,omitempty"`
	Order         []WeightSpec       `json:"order,omitempty"`
	SiteScores    map[string]float64 `json:"site_scores,omitempty"`
}

type StalledSpec struct {
	MinAddedAgeSeconds        int64 `json:"min_added_age_seconds"`
	MinLastActivityAgeSeconds int64 `json:"min_last_activity_age_seconds"`
}

// PolicySpec is the editable body of a policy; identity, revision and the
// live flag are owned by the store.
type PolicySpec struct {
	Triggers          []TriggerSpec  `json:"triggers,omitempty"`
	Watermark         *WatermarkSpec `json:"watermark,omitempty"`
	Stalled           *StalledSpec   `json:"stalled,omitempty"`
	DeleteMode        DeleteMode     `json:"delete_mode"`
	ConfirmForSeconds int64          `json:"confirm_for_seconds,omitempty"`
	IntervalSeconds   int64          `json:"interval_seconds,omitempty"`
}

type HitAndRunSpec struct {
	MinSeedingTimeSeconds int64   `json:"min_seeding_time_seconds,omitempty"`
	MinRatio              float64 `json:"min_ratio,omitempty"`
}

type SafetySpec struct {
	ProtectedCategories []string                 `json:"protected_categories,omitempty"`
	ProtectedSites      []string                 `json:"protected_sites,omitempty"`
	HitAndRun           map[string]HitAndRunSpec `json:"hit_and_run,omitempty"`
}

// maxSeconds keeps conversions far from time.Duration overflow (~292 years).
const maxSeconds = 100 * 365 * 24 * 3600

func duration(value int64, field string) (time.Duration, error) {
	if value < 0 || value > maxSeconds {
		return 0, invalid("%s must be between 0 and %d seconds", field, int64(maxSeconds))
	}
	return time.Duration(value) * time.Second, nil
}

func (spec ThresholdSpec) threshold() (Threshold, error) {
	seconds, err := duration(spec.Seconds, string(spec.Metric))
	if err != nil {
		return Threshold{}, err
	}
	return Threshold{Metric: spec.Metric, Op: spec.Op, Value: spec.Value, Duration: seconds}, nil
}

func thresholdsFrom(specs []ThresholdSpec) ([]Threshold, error) {
	result := make([]Threshold, 0, len(specs))
	for _, spec := range specs {
		threshold, err := spec.threshold()
		if err != nil {
			return nil, err
		}
		result = append(result, threshold)
	}
	return result, nil
}

// Policy converts the spec into a validated Policy.
func (spec PolicySpec) Policy(id, name string, revision int, live bool) (Policy, error) {
	policy := Policy{ID: id, Name: name, Revision: revision, Live: live, DeleteMode: spec.DeleteMode}
	var err error
	if policy.ConfirmFor, err = duration(spec.ConfirmForSeconds, "confirm_for_seconds"); err != nil {
		return Policy{}, err
	}
	if policy.Interval, err = duration(spec.IntervalSeconds, "interval_seconds"); err != nil {
		return Policy{}, err
	}
	for _, trigger := range spec.Triggers {
		all, err := thresholdsFrom(trigger.All)
		if err != nil {
			return Policy{}, err
		}
		policy.Triggers = append(policy.Triggers, Trigger{All: all})
	}
	if watermark := spec.Watermark; watermark != nil {
		eligible, err := thresholdsFrom(watermark.Eligible)
		if err != nil {
			return Policy{}, err
		}
		order := make([]Weight, 0, len(watermark.Order))
		for _, weight := range watermark.Order {
			order = append(order, Weight{Metric: weight.Metric, Multiplier: weight.Multiplier})
		}
		policy.Watermark = &Watermark{MinFreeBytes: watermark.MinFreeBytes, FreeSpacePath: watermark.FreeSpacePath, Eligible: eligible, Order: order, SiteScores: watermark.SiteScores}
	}
	if stalled := spec.Stalled; stalled != nil {
		added, err := duration(stalled.MinAddedAgeSeconds, "min_added_age_seconds")
		if err != nil {
			return Policy{}, err
		}
		activity, err := duration(stalled.MinLastActivityAgeSeconds, "min_last_activity_age_seconds")
		if err != nil {
			return Policy{}, err
		}
		policy.Stalled = &StalledCleanup{MinAddedAge: added, MinLastActivityAge: activity}
	}
	return policy, policy.Validate()
}

// Safety converts the spec into validated safety settings.
func (spec SafetySpec) Safety() (Safety, error) {
	safety := Safety{ProtectedCategories: spec.ProtectedCategories, ProtectedSites: spec.ProtectedSites}
	if len(spec.HitAndRun) > 0 {
		safety.HitAndRun = make(map[string]HitAndRun, len(spec.HitAndRun))
		for site, rule := range spec.HitAndRun {
			seeding, err := duration(rule.MinSeedingTimeSeconds, "min_seeding_time_seconds")
			if err != nil {
				return Safety{}, err
			}
			safety.HitAndRun[site] = HitAndRun{MinSeedingTime: seeding, MinRatio: rule.MinRatio}
		}
	}
	return safety, safety.validate()
}

// DecodePolicySpec strictly decodes a stored or submitted policy body.
func DecodePolicySpec(data []byte) (PolicySpec, error) {
	var spec PolicySpec
	if err := strictJSON(data, &spec); err != nil {
		return PolicySpec{}, invalid("policy body: %v", err)
	}
	return spec, nil
}

// DecodeSafetySpec strictly decodes stored or submitted safety settings.
func DecodeSafetySpec(data []byte) (SafetySpec, error) {
	var spec SafetySpec
	if err := strictJSON(data, &spec); err != nil {
		return SafetySpec{}, invalid("safety settings: %v", err)
	}
	return spec, nil
}

func strictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytesReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errTrailing
	}
	return nil
}
