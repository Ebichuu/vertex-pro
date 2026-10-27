// Package syncfixture builds deterministic qB sync/maindata payloads for sync
// benchmarks and large-response tests. Torrents carry the full qB 4.3–5.2
// field set so encode/decode/merge costs match production payloads.
package syncfixture

import (
	"encoding/json"
	"fmt"

	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

// Hash returns a stable 40-character v1 infohash for torrent i.
func Hash(i int) string { return fmt.Sprintf("%040x", i+1) }

// Torrent returns a complete qB maindata torrent object for torrent i.
func Torrent(i int) map[string]any {
	hash := Hash(i)
	size := float64(1<<30 + i*4096)
	return map[string]any{
		"added_on": float64(1761000000 + i), "amount_left": float64(0), "auto_tmm": false, "availability": float64(-1),
		"category": "brush", "completed": size, "completion_on": float64(1761000600 + i), "content_path": "/data/brush/Some.Release.Name.2026.1080p.WEB-DL-" + hash[:8],
		"dl_limit": float64(0), "dlspeed": float64(0), "download_path": "", "downloaded": size, "downloaded_session": float64(0), "eta": float64(8640000),
		"f_l_piece_prio": false, "force_start": false, "has_metadata": true, "inactive_seeding_time_limit": float64(-2), "infohash_v1": hash, "infohash_v2": "",
		"last_activity": float64(1761001000 + i), "magnet_uri": "magnet:?xt=urn:btih:" + hash + "&dn=Some.Release.Name", "max_inactive_seeding_time": float64(-1),
		"max_ratio": float64(-1), "max_seeding_time": float64(-1), "name": "Some.Release.Name.2026.1080p.WEB-DL-" + hash[:8], "num_complete": float64(35), "num_incomplete": float64(2),
		"num_leechs": float64(0), "num_seeds": float64(0), "popularity": float64(0.42), "priority": float64(0), "private": true, "progress": float64(1), "ratio": float64(1.37),
		"ratio_limit": float64(-2), "reannounce": float64(1500), "root_path": "/data/brush/Some.Release.Name", "save_path": "/data/brush", "seeding_time": float64(86400 + i),
		"seeding_time_limit": float64(-2), "seen_complete": float64(1761000600 + i), "seq_dl": false, "size": size, "state": "stalledUP", "super_seeding": false,
		"tags": "vertex", "time_active": float64(90000 + i), "total_size": size, "tracker": "https://tracker.example.org/announce.php?passkey=0000", "trackers_count": float64(1),
		"up_limit": float64(0), "uploaded": size * 1.37, "uploaded_session": float64(0), "upspeed": float64(0),
	}
}

// ServerState returns a qB server_state object.
func ServerState() map[string]any {
	return map[string]any{
		"alltime_dl": float64(1 << 40), "alltime_ul": float64(3 << 40), "connection_status": "connected", "dht_nodes": float64(0), "dl_info_data": float64(1 << 34),
		"dl_info_speed": float64(0), "free_space_on_disk": float64(2 << 40), "global_ratio": "2.71", "queueing": false, "up_info_data": float64(1 << 35), "up_info_speed": float64(1 << 24),
		"use_alt_speed_limits": false, "refresh_interval": float64(1500),
	}
}

func fullTorrents(n int) map[string]map[string]any {
	torrents := make(map[string]map[string]any, n)
	for i := 0; i < n; i++ {
		torrents[Hash(i)] = Torrent(i)
	}
	return torrents
}

// incrementalTorrents changes a few transfer fields on `changed` of n
// torrents, rotating which torrents are active by rid, as qB does for active swarms.
func incrementalTorrents(n, changed int, rid uint64) map[string]map[string]any {
	torrents := make(map[string]map[string]any, changed)
	for k := 0; k < changed && n > 0; k++ {
		i := (int(rid)*changed + k) % n
		torrents[Hash(i)] = map[string]any{
			"upspeed": float64(1<<20 + k), "uploaded": float64(int(rid)<<20 + k), "uploaded_session": float64(int(rid) << 20),
			"num_leechs": float64(k % 7), "num_incomplete": float64(k % 11), "last_activity": float64(1761100000 + int(rid)), "ratio": float64(1.5), "state": "uploading",
		}
	}
	return torrents
}

func incrementalServerState(rid uint64) map[string]any {
	return map[string]any{"up_info_speed": float64(rid << 20), "up_info_data": float64(rid << 30)}
}

// Deltas decodes generic torrent objects as the proxy would, panicking on
// invalid fixtures.
func Deltas(raw map[string]map[string]any) map[string]syncmerge.TorrentDelta {
	torrents, err := syncmerge.TorrentDeltas(raw)
	if err != nil {
		panic(err)
	}
	return torrents
}

// FullDelta is the decoded form of a full update with n torrents.
func FullDelta(n int, rid uint64) syncmerge.Delta {
	return syncmerge.Delta{RID: rid, FullUpdate: true, Torrents: Deltas(fullTorrents(n)), ServerState: ServerState()}
}

// IncrementalDelta is the decoded form of IncrementalMaindataJSON.
func IncrementalDelta(n, changed int, rid uint64) syncmerge.Delta {
	return syncmerge.Delta{RID: rid, Torrents: Deltas(incrementalTorrents(n, changed, rid)), ServerState: incrementalServerState(rid)}
}

// FullMaindataJSON is qB's raw full sync/maindata response for n torrents.
func FullMaindataJSON(n int, rid uint64) []byte {
	return maindataJSON(map[string]any{"rid": rid, "full_update": true, "torrents": fullTorrents(n), "server_state": ServerState()})
}

// IncrementalMaindataJSON is qB's raw incremental response.
func IncrementalMaindataJSON(n, changed int, rid uint64) []byte {
	return maindataJSON(map[string]any{"rid": rid, "torrents": incrementalTorrents(n, changed, rid), "server_state": incrementalServerState(rid)})
}

func maindataJSON(payload map[string]any) []byte {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return encoded
}
