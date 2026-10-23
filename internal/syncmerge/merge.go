package syncmerge

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

type Torrent struct {
	ID              string
	Fields          map[string]any
	AmountLeftState string
	AmountLeft      *int64
}

type Snapshot struct {
	RID         uint64
	Torrents    map[string]Torrent
	ServerState map[string]any `json:",omitempty"`
}

type Delta struct {
	RID         uint64
	FullUpdate  bool
	Torrents    map[string]map[string]any
	Removed     []string
	ServerState map[string]any
}

type MergeResult struct {
	Applied  bool
	Ignored  bool
	Reset    bool
	Snapshot Snapshot
}

func Merge(previous Snapshot, delta Delta) (MergeResult, error) {
	if delta.RID == 0 || delta.FullUpdate {
		next := Snapshot{RID: delta.RID, Torrents: map[string]Torrent{}, ServerState: cloneFields(delta.ServerState)}
		if err := applyTorrents(&next, delta.Torrents, false); err != nil {
			return MergeResult{}, err
		}
		for _, id := range delta.Removed {
			delete(next.Torrents, id)
		}
		return MergeResult{Applied: true, Reset: true, Snapshot: next}, nil
	}
	if previous.Torrents == nil {
		return MergeResult{}, errors.New("incremental delta requires a baseline")
	}
	if delta.RID <= previous.RID {
		return MergeResult{Ignored: true, Snapshot: cloneSnapshot(previous)}, nil
	}
	next := cloneSnapshot(previous)
	if next.ServerState == nil {
		next.ServerState = make(map[string]any)
	}
	for key, value := range delta.ServerState {
		next.ServerState[key] = value
	}
	if err := applyTorrents(&next, delta.Torrents, true); err != nil {
		return MergeResult{}, err
	}
	for _, id := range delta.Removed {
		delete(next.Torrents, id)
	}
	next.RID = delta.RID
	return MergeResult{Applied: true, Snapshot: next}, nil
}

func applyTorrents(snapshot *Snapshot, raw map[string]map[string]any, inherit bool) error {
	for id, fields := range raw {
		if id == "" {
			return errors.New("torrent ID is required")
		}
		current, exists := snapshot.Torrents[id]
		if !exists || !inherit {
			current = Torrent{ID: id, Fields: map[string]any{}}
		}
		if current.Fields == nil {
			current.Fields = map[string]any{}
		}
		for key, value := range fields {
			current.Fields[key] = value
		}
		if err := normalizeAmountLeft(&current); err != nil {
			return fmt.Errorf("torrent %s: %w", id, err)
		}
		snapshot.Torrents[id] = current
	}
	return nil
}

func normalizeAmountLeft(torrent *Torrent) error {
	value, exists := torrent.Fields["amount_left"]
	if !exists || value == nil {
		torrent.AmountLeftState = "UNKNOWN"
		torrent.AmountLeft = nil
		return nil
	}
	parsed, ok := parseInteger(value)
	if !ok || parsed < 0 {
		torrent.AmountLeftState = "UNKNOWN"
		torrent.AmountLeft = nil
		return nil
	}
	torrent.AmountLeftState = "KNOWN"
	torrent.AmountLeft = &parsed
	return nil
}

func parseInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if typed != math.Trunc(typed) || typed > math.MaxInt64 || typed < math.MinInt64 {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func cloneFields(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneSnapshot(source Snapshot) Snapshot {
	clone := Snapshot{RID: source.RID, Torrents: make(map[string]Torrent, len(source.Torrents)), ServerState: cloneFields(source.ServerState)}
	for id, torrent := range source.Torrents {
		fields := make(map[string]any, len(torrent.Fields))
		for key, value := range torrent.Fields {
			fields[key] = value
		}
		torrent.Fields = fields
		if torrent.AmountLeft != nil {
			value := *torrent.AmountLeft
			torrent.AmountLeft = &value
		}
		clone.Torrents[id] = torrent
	}
	return clone
}
