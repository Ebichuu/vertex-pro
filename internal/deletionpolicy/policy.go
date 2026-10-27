// Package deletionpolicy defines Vertex deletion policies and compiles them
// into qui automations. The compiler always wraps a policy in the safety
// envelope (see envelope.go); a policy cannot remove or weaken it.
//
// The package is pure: no HTTP, no database. Syncing compiled automations to
// qui and choosing when a policy goes live belong to later stages (D3).
package deletionpolicy

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// Metric is a torrent fact a threshold compares. Each maps to one qui field.
type Metric string

const (
	SeedingTime      Metric = "seeding_time"       // seconds seeded (qB seeding_time)
	AddedAge         Metric = "added_age"          // time since the torrent was added
	LastActivityAge  Metric = "last_activity_age"  // time since last up/down transfer
	Ratio            Metric = "ratio"              // qB share ratio
	UploadedOverSize Metric = "uploaded_over_size" // uploaded / total size; cross-seed safe ratio
	Uploaded         Metric = "uploaded"           // bytes
	UpSpeedAvg       Metric = "up_speed_avg"       // lifetime average upload, bytes/s
	UpSpeed          Metric = "up_speed"           // current upload, bytes/s
	NumLeechs        Metric = "num_leechs"         // connected leechers
	NumIncomplete    Metric = "num_incomplete"     // leechers in the swarm
	NumSeeds         Metric = "num_seeds"          // connected seeders
	NumComplete      Metric = "num_complete"       // seeders in the swarm
	Size             Metric = "size"               // bytes
	Unregistered     Metric = "unregistered"       // tracker reports the torrent unregistered
)

type metricSpec struct {
	field    string
	duration bool // value is a time.Duration, sent as whole seconds
	float    bool // value may be fractional
	sortable bool // usable as a watermark score field
}

var metrics = map[Metric]metricSpec{
	SeedingTime:      {field: "SEEDING_TIME", duration: true, sortable: true},
	AddedAge:         {field: "ADDED_ON_AGE", duration: true, sortable: true},
	LastActivityAge:  {field: "LAST_ACTIVITY_AGE", duration: true, sortable: true},
	Ratio:            {field: "RATIO", float: true, sortable: true},
	UploadedOverSize: {field: "UPLOADED_OVER_SIZE", float: true},
	Uploaded:         {field: "UPLOADED", sortable: true},
	UpSpeedAvg:       {field: "UP_SPEED_AVG", sortable: true},
	UpSpeed:          {field: "UP_SPEED", sortable: true},
	NumLeechs:        {field: "NUM_LEECHS", sortable: true},
	NumIncomplete:    {field: "NUM_INCOMPLETE", sortable: true},
	NumSeeds:         {field: "NUM_SEEDS", sortable: true},
	NumComplete:      {field: "NUM_COMPLETE", sortable: true},
	Size:             {field: "SIZE", sortable: true},
	Unregistered:     {field: "IS_UNREGISTERED"},
}

// Comparison is a threshold operator.
type Comparison string

const (
	GreaterThan    Comparison = ">"
	GreaterOrEqual Comparison = ">="
	LessThan       Comparison = "<"
	LessOrEqual    Comparison = "<="
)

var comparisonOperators = map[Comparison]string{
	GreaterThan: "GREATER_THAN", GreaterOrEqual: "GREATER_THAN_OR_EQUAL", LessThan: "LESS_THAN", LessOrEqual: "LESS_THAN_OR_EQUAL",
}

// Threshold compares one metric with a value. Duration metrics use Duration;
// other metrics use Value. Unregistered takes neither.
type Threshold struct {
	Metric   Metric
	Op       Comparison
	Value    float64
	Duration time.Duration
}

// Trigger deletes a torrent when all of its thresholds hold.
type Trigger struct {
	All []Threshold
}

// DeleteMode controls what qui removes. Deleting other torrents' data
// (qui's "include cross-seeds" mode) is deliberately not offered: it can
// reach torrents outside the safety envelope.
type DeleteMode string

const (
	KeepFiles                   DeleteMode = "keep_files"
	WithFiles                   DeleteMode = "with_files"
	WithFilesPreserveCrossSeeds DeleteMode = "with_files_preserve_cross_seeds"
)

var deleteModes = map[DeleteMode]string{
	KeepFiles: "delete", WithFiles: "deleteWithFiles", WithFilesPreserveCrossSeeds: "deleteWithFilesPreserveCrossSeeds",
}

// Weight adds Multiplier × metric value to a torrent's deletion score.
type Weight struct {
	Metric     Metric
	Multiplier float64
}

// Watermark frees disk space: while projected free space is below
// MinFreeBytes, qui deletes Eligible torrents highest score first until the
// target is met.
type Watermark struct {
	MinFreeBytes int64
	// FreeSpacePath reads free space from a path qui can see; empty uses the
	// free space qBittorrent reports for its default save path.
	FreeSpacePath string
	Eligible      []Threshold
	Order         []Weight
	// SiteScores adds a flat score to torrents of a site; a higher score is
	// deleted earlier, so low-value sites get positive scores.
	SiteScores map[string]float64
}

// StalledCleanup removes incomplete downloads that stopped making progress.
// It never applies to Hit-and-Run sites.
type StalledCleanup struct {
	MinAddedAge        time.Duration
	MinLastActivityAge time.Duration
}

// Policy is one Vertex deletion policy.
type Policy struct {
	ID       string
	Name     string
	Revision int
	// Live is set only after an operator has reviewed qui's preview; until
	// then every compiled automation runs in qui dry-run mode.
	Live       bool
	Triggers   []Trigger
	Watermark  *Watermark
	Stalled    *StalledCleanup
	DeleteMode DeleteMode
	// ConfirmFor requires the delete condition to stay matched across qui
	// runs for this long before deleting (qui conditionMatchDurationSeconds).
	ConfirmFor time.Duration
	// Interval between qui evaluations; zero uses DefaultInterval.
	Interval time.Duration
}

const (
	DefaultInterval = 5 * time.Minute
	MinInterval     = time.Minute
	MaxInterval     = 24 * time.Hour
	// MinHold is the smallest seeding/added age a trigger may anchor on.
	MinHold = 10 * time.Minute
	// MinStalledAge bounds stalled-download cleanup from below.
	MinStalledAge = 30 * time.Minute
)

var (
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	sitePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// ErrInvalid wraps every policy validation failure.
var ErrInvalid = errors.New("invalid deletion policy")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Validate checks the policy independently of the envelope.
func (policy Policy) Validate() error {
	if !idPattern.MatchString(policy.ID) {
		return invalid("policy ID must match %s", idPattern)
	}
	if strings.TrimSpace(policy.Name) == "" || len(policy.Name) > 200 {
		return invalid("policy name is required (at most 200 bytes)")
	}
	if policy.Revision <= 0 {
		return invalid("policy revision must be positive")
	}
	if _, ok := deleteModes[policy.DeleteMode]; !ok {
		return invalid("unsupported delete mode %q", policy.DeleteMode)
	}
	if len(policy.Triggers) == 0 && policy.Watermark == nil && policy.Stalled == nil {
		return invalid("policy deletes nothing: add a trigger, a watermark or stalled cleanup")
	}
	if policy.Interval != 0 && (policy.Interval < MinInterval || policy.Interval > MaxInterval || policy.Interval%time.Second != 0) {
		return invalid("interval must be whole seconds between %s and %s", MinInterval, MaxInterval)
	}
	if policy.ConfirmFor < 0 || policy.ConfirmFor > 7*24*time.Hour || policy.ConfirmFor%time.Second != 0 {
		return invalid("confirm duration must be whole seconds between 0 and 7 days")
	}
	for index, trigger := range policy.Triggers {
		if len(trigger.All) == 0 {
			return invalid("trigger %d has no thresholds", index)
		}
		anchored := false
		for _, threshold := range trigger.All {
			if err := threshold.validate(); err != nil {
				return invalid("trigger %d: %v", index, err)
			}
			anchored = anchored || threshold.anchors()
		}
		if !anchored {
			return invalid("trigger %d needs a hold floor: seeding time or added age >= %s, ratio > 0, or unregistered", index, MinHold)
		}
	}
	if watermark := policy.Watermark; watermark != nil {
		if watermark.MinFreeBytes <= 0 {
			return invalid("watermark free-space target must be positive")
		}
		if watermark.FreeSpacePath != "" && !strings.HasPrefix(watermark.FreeSpacePath, "/") && !windowsPath(watermark.FreeSpacePath) {
			return invalid("watermark free-space path must be absolute")
		}
		for _, threshold := range watermark.Eligible {
			if err := threshold.validate(); err != nil {
				return invalid("watermark eligibility: %v", err)
			}
		}
		if len(watermark.Order) == 0 && len(watermark.SiteScores) == 0 {
			return invalid("watermark needs an order: at least one weight or site score")
		}
		for _, weight := range watermark.Order {
			spec, ok := metrics[weight.Metric]
			if !ok || !spec.sortable {
				return invalid("metric %q cannot weight the watermark order", weight.Metric)
			}
			if !finite(weight.Multiplier) || weight.Multiplier == 0 {
				return invalid("watermark weight for %s must be a non-zero finite number", weight.Metric)
			}
		}
		for site, score := range watermark.SiteScores {
			if !sitePattern.MatchString(site) || !finite(score) {
				return invalid("invalid watermark site score for %q", site)
			}
		}
	}
	if stalled := policy.Stalled; stalled != nil {
		if stalled.MinAddedAge < MinStalledAge || stalled.MinLastActivityAge < MinStalledAge {
			return invalid("stalled cleanup ages must be at least %s", MinStalledAge)
		}
		if stalled.MinAddedAge%time.Second != 0 || stalled.MinLastActivityAge%time.Second != 0 {
			return invalid("stalled cleanup ages must be whole seconds")
		}
	}
	return nil
}

func (threshold Threshold) validate() error {
	spec, ok := metrics[threshold.Metric]
	if !ok {
		return fmt.Errorf("unknown metric %q", threshold.Metric)
	}
	if threshold.Metric == Unregistered {
		if threshold.Op != "" || threshold.Value != 0 || threshold.Duration != 0 {
			return errors.New("unregistered takes no operator or value")
		}
		return nil
	}
	if _, ok := comparisonOperators[threshold.Op]; !ok {
		return fmt.Errorf("%s: unsupported comparison %q", threshold.Metric, threshold.Op)
	}
	if spec.duration {
		if threshold.Value != 0 {
			return fmt.Errorf("%s takes a duration, not a value", threshold.Metric)
		}
		if threshold.Duration < 0 || threshold.Duration%time.Second != 0 {
			return fmt.Errorf("%s must be a non-negative whole number of seconds", threshold.Metric)
		}
		return nil
	}
	if threshold.Duration != 0 {
		return fmt.Errorf("%s takes a value, not a duration", threshold.Metric)
	}
	if !finite(threshold.Value) || threshold.Value < 0 {
		return fmt.Errorf("%s must be a finite non-negative value", threshold.Metric)
	}
	if !spec.float && threshold.Value != math.Trunc(threshold.Value) {
		return fmt.Errorf("%s must be a whole number", threshold.Metric)
	}
	if !spec.float && threshold.Value > math.MaxInt64/2 {
		return fmt.Errorf("%s is out of range", threshold.Metric)
	}
	return nil
}

// anchors reports whether the threshold alone keeps fresh torrents safe.
func (threshold Threshold) anchors() bool {
	lower := threshold.Op == GreaterThan || threshold.Op == GreaterOrEqual
	switch threshold.Metric {
	case Unregistered:
		return true
	case SeedingTime, AddedAge:
		return lower && threshold.Duration >= MinHold
	case Ratio, UploadedOverSize:
		return lower && threshold.Value > 0
	}
	return false
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

var errTrailing = errors.New("unexpected trailing data")

func bytesReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }

func windowsPath(path string) bool {
	return len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}
