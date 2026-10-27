package syncmerge

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

// Field identifies one qB torrent field kept in the snapshot. Only fields with
// a reader are decoded; everything else in maindata is skipped by the decoder.
type Field uint32

const (
	FieldName Field = 1 << iota
	FieldState
	FieldCategory
	FieldTags
	FieldTracker
	FieldSavePath
	FieldSize
	FieldTotalSize
	FieldCompleted
	FieldDownloaded
	FieldUploaded
	FieldDLSpeed
	FieldUPSpeed
	FieldDLLimit
	FieldUPLimit
	FieldNumSeeds
	FieldNumLeechs
	FieldNumComplete
	FieldNumIncomplete
	FieldAddedOn
	FieldCompletionOn
	FieldLastActivity
	FieldSeedingTime
	FieldTimeActive
	FieldProgress
	FieldRatio
	FieldAmountLeft
)

// Torrent is one task in a snapshot. Known records which fields qB has
// reported with a valid value; a zero field without its bit is "not reported".
type Torrent struct {
	ID    string
	Known Field

	Name, State, Category, Tags, Tracker, SavePath string

	Size, TotalSize, Completed, Downloaded, Uploaded int64
	DLSpeed, UPSpeed, DLLimit, UPLimit               int64
	NumSeeds, NumLeechs, NumComplete, NumIncomplete  int64
	AddedOn, CompletionOn, LastActivity              int64
	SeedingTime, TimeActive                          int64
	Progress, Ratio                                  float64

	AmountLeftState string
	AmountLeft      *int64
}

// Has reports whether qB has reported a valid value for field.
func (torrent Torrent) Has(field Field) bool { return torrent.Known&field != 0 }

// Value returns a reported field by its qB maindata name, for evidence tools
// that work with raw field names. Unknown names and unreported fields are absent.
func (torrent Torrent) Value(name string) (any, bool) {
	field, ok := fieldByName[name]
	if !ok || !torrent.Has(field) {
		return nil, false
	}
	switch field {
	case FieldName:
		return torrent.Name, true
	case FieldState:
		return torrent.State, true
	case FieldCategory:
		return torrent.Category, true
	case FieldTags:
		return torrent.Tags, true
	case FieldTracker:
		return torrent.Tracker, true
	case FieldSavePath:
		return torrent.SavePath, true
	case FieldProgress:
		return torrent.Progress, true
	case FieldRatio:
		return torrent.Ratio, true
	case FieldAmountLeft:
		if torrent.AmountLeft == nil {
			return nil, true
		}
		return *torrent.AmountLeft, true
	}
	return *torrent.integer(field), true
}

func (torrent *Torrent) integer(field Field) *int64 {
	switch field {
	case FieldSize:
		return &torrent.Size
	case FieldTotalSize:
		return &torrent.TotalSize
	case FieldCompleted:
		return &torrent.Completed
	case FieldDownloaded:
		return &torrent.Downloaded
	case FieldUploaded:
		return &torrent.Uploaded
	case FieldDLSpeed:
		return &torrent.DLSpeed
	case FieldUPSpeed:
		return &torrent.UPSpeed
	case FieldDLLimit:
		return &torrent.DLLimit
	case FieldUPLimit:
		return &torrent.UPLimit
	case FieldNumSeeds:
		return &torrent.NumSeeds
	case FieldNumLeechs:
		return &torrent.NumLeechs
	case FieldNumComplete:
		return &torrent.NumComplete
	case FieldNumIncomplete:
		return &torrent.NumIncomplete
	case FieldAddedOn:
		return &torrent.AddedOn
	case FieldCompletionOn:
		return &torrent.CompletionOn
	case FieldLastActivity:
		return &torrent.LastActivity
	case FieldSeedingTime:
		return &torrent.SeedingTime
	case FieldTimeActive:
		return &torrent.TimeActive
	}
	return nil
}

var fieldByName = map[string]Field{
	"name": FieldName, "state": FieldState, "category": FieldCategory, "tags": FieldTags, "tracker": FieldTracker, "save_path": FieldSavePath,
	"size": FieldSize, "total_size": FieldTotalSize, "completed": FieldCompleted, "downloaded": FieldDownloaded, "uploaded": FieldUploaded,
	"dlspeed": FieldDLSpeed, "upspeed": FieldUPSpeed, "dl_limit": FieldDLLimit, "up_limit": FieldUPLimit,
	"num_seeds": FieldNumSeeds, "num_leechs": FieldNumLeechs, "num_complete": FieldNumComplete, "num_incomplete": FieldNumIncomplete,
	"added_on": FieldAddedOn, "completion_on": FieldCompletionOn, "last_activity": FieldLastActivity, "seeding_time": FieldSeedingTime, "time_active": FieldTimeActive,
	"progress": FieldProgress, "ratio": FieldRatio, "amount_left": FieldAmountLeft,
}

// TorrentDelta is one torrent object of a maindata response. A nil field was
// not sent (or was null). Numbers decode as float64, exactly as the former
// map[string]any decoding did. amount_left stays raw so that an explicit null
// or a numeric string keeps its previous meaning (UNKNOWN / parsed integer).
type TorrentDelta struct {
	Name     *string `json:"name"`
	State    *string `json:"state"`
	Category *string `json:"category"`
	Tags     *string `json:"tags"`
	Tracker  *string `json:"tracker"`
	SavePath *string `json:"save_path"`

	Size          *float64 `json:"size"`
	TotalSize     *float64 `json:"total_size"`
	Completed     *float64 `json:"completed"`
	Downloaded    *float64 `json:"downloaded"`
	Uploaded      *float64 `json:"uploaded"`
	DLSpeed       *float64 `json:"dlspeed"`
	UPSpeed       *float64 `json:"upspeed"`
	DLLimit       *float64 `json:"dl_limit"`
	UPLimit       *float64 `json:"up_limit"`
	NumSeeds      *float64 `json:"num_seeds"`
	NumLeechs     *float64 `json:"num_leechs"`
	NumComplete   *float64 `json:"num_complete"`
	NumIncomplete *float64 `json:"num_incomplete"`
	AddedOn       *float64 `json:"added_on"`
	CompletionOn  *float64 `json:"completion_on"`
	LastActivity  *float64 `json:"last_activity"`
	SeedingTime   *float64 `json:"seeding_time"`
	TimeActive    *float64 `json:"time_active"`
	Progress      *float64 `json:"progress"`
	Ratio         *float64 `json:"ratio"`

	AmountLeft json.RawMessage `json:"amount_left"`
}

// TorrentDeltas converts generic torrent objects (tests, fixtures, probes)
// through the same JSON decoding the proxy uses.
func TorrentDeltas(raw map[string]map[string]any) (map[string]TorrentDelta, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var torrents map[string]TorrentDelta
	if err := json.Unmarshal(encoded, &torrents); err != nil {
		return nil, err
	}
	return torrents, nil
}

func (torrent *Torrent) apply(delta TorrentDelta) {
	text := func(target *string, value *string, field Field) {
		if value != nil {
			*target = *value
			torrent.Known |= field
		}
	}
	text(&torrent.Name, delta.Name, FieldName)
	text(&torrent.State, delta.State, FieldState)
	text(&torrent.Category, delta.Category, FieldCategory)
	text(&torrent.Tags, delta.Tags, FieldTags)
	text(&torrent.Tracker, delta.Tracker, FieldTracker)
	text(&torrent.SavePath, delta.SavePath, FieldSavePath)

	integer := func(value *float64, field Field) {
		if value == nil {
			return
		}
		// A non-integral value is reported but not valid: keep it unknown.
		if parsed, ok := floatInteger(*value); ok {
			*torrent.integer(field) = parsed
			torrent.Known |= field
		} else {
			*torrent.integer(field) = 0
			torrent.Known &^= field
		}
	}
	integer(delta.Size, FieldSize)
	integer(delta.TotalSize, FieldTotalSize)
	integer(delta.Completed, FieldCompleted)
	integer(delta.Downloaded, FieldDownloaded)
	integer(delta.Uploaded, FieldUploaded)
	integer(delta.DLSpeed, FieldDLSpeed)
	integer(delta.UPSpeed, FieldUPSpeed)
	integer(delta.DLLimit, FieldDLLimit)
	integer(delta.UPLimit, FieldUPLimit)
	integer(delta.NumSeeds, FieldNumSeeds)
	integer(delta.NumLeechs, FieldNumLeechs)
	integer(delta.NumComplete, FieldNumComplete)
	integer(delta.NumIncomplete, FieldNumIncomplete)
	integer(delta.AddedOn, FieldAddedOn)
	integer(delta.CompletionOn, FieldCompletionOn)
	integer(delta.LastActivity, FieldLastActivity)
	integer(delta.SeedingTime, FieldSeedingTime)
	integer(delta.TimeActive, FieldTimeActive)

	if delta.Progress != nil {
		torrent.Progress = *delta.Progress
		torrent.Known |= FieldProgress
	}
	if delta.Ratio != nil {
		torrent.Ratio = *delta.Ratio
		torrent.Known |= FieldRatio
	}
	if delta.AmountLeft != nil {
		torrent.Known |= FieldAmountLeft
		if parsed, ok := parseAmountLeft(delta.AmountLeft); ok {
			torrent.AmountLeftState, torrent.AmountLeft = "KNOWN", &parsed
		} else {
			torrent.AmountLeftState, torrent.AmountLeft = "UNKNOWN", nil
		}
	}
}

func floatInteger(value float64) (int64, bool) {
	if value != math.Trunc(value) || value >= math.MaxInt64 || value < math.MinInt64 {
		return 0, false
	}
	return int64(value), true
}

// parseAmountLeft accepts a non-negative integral JSON number or numeric
// string; null and anything else are UNKNOWN.
func parseAmountLeft(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	var parsed int64
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return 0, false
		}
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return 0, false
		}
		parsed = value
	} else {
		value, err := strconv.ParseFloat(string(raw), 64)
		if err != nil {
			return 0, false
		}
		integer, ok := floatInteger(value)
		if !ok {
			return 0, false
		}
		parsed = integer
	}
	return parsed, parsed >= 0
}
