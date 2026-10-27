package syncmerge

import (
	"errors"
	"maps"
)

// Snapshot is immutable once returned by Merge: successive snapshots share
// unchanged *Torrent values, so callers must never mutate them. Pointers keep
// the copy-on-write index clone to one word per torrent; Torrent exceeds the
// map's inline value size, so storing it by value costs a heap object per entry.
type Snapshot struct {
	RID         uint64
	Torrents    map[string]*Torrent
	ServerState map[string]any `json:",omitempty"`
}

type Delta struct {
	RID         uint64
	FullUpdate  bool
	Torrents    map[string]TorrentDelta
	Removed     []string
	ServerState map[string]any
	// BodyDigest is the hex sha256 of the raw response, when the fetcher saw one.
	BodyDigest string
}

type MergeResult struct {
	Applied  bool
	Ignored  bool
	Reset    bool
	Snapshot Snapshot
}

func Merge(previous Snapshot, delta Delta) (MergeResult, error) {
	if delta.RID == 0 || delta.FullUpdate {
		next := Snapshot{RID: delta.RID, Torrents: make(map[string]*Torrent, len(delta.Torrents)), ServerState: maps.Clone(delta.ServerState)}
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
		return MergeResult{Ignored: true, Snapshot: previous}, nil
	}
	// Copy-on-write: only the index is cloned; changed torrents are replaced by
	// new objects and the AmountLeft pointer is replaced, never written through.
	next := Snapshot{RID: delta.RID, Torrents: maps.Clone(previous.Torrents), ServerState: maps.Clone(previous.ServerState)}
	if next.ServerState == nil {
		next.ServerState = make(map[string]any, len(delta.ServerState))
	}
	maps.Copy(next.ServerState, delta.ServerState)
	if err := applyTorrents(&next, delta.Torrents, true); err != nil {
		return MergeResult{}, err
	}
	for _, id := range delta.Removed {
		delete(next.Torrents, id)
	}
	return MergeResult{Applied: true, Snapshot: next}, nil
}

func applyTorrents(snapshot *Snapshot, raw map[string]TorrentDelta, inherit bool) error {
	for id, fields := range raw {
		if id == "" {
			return errors.New("torrent ID is required")
		}
		current := Torrent{ID: id, AmountLeftState: "UNKNOWN"}
		if previous, exists := snapshot.Torrents[id]; exists && inherit {
			current = *previous
		}
		current.apply(fields)
		snapshot.Torrents[id] = &current
	}
	return nil
}
