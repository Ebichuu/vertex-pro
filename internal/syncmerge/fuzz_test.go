package syncmerge

import (
	"encoding/json"
	"testing"
)

// FuzzMerge 是 HARD-05 的 qB sync delta fuzz：任意 RID/全量标志/torrent
// 字典/删除列表组合不得 panic，合并结果必须自洽（应用了就非忽略）。
func FuzzMerge(f *testing.F) {
	f.Add(uint64(1), true, `{}`, `[]`)
	f.Add(uint64(2), false, `{"t1":{"name":"a","hash":"h1"}}`, `["t2"]`)
	f.Add(uint64(0), false, `{"t1":null}`, `[]`)
	f.Add(uint64(3), false, `{"t1":{"amount_left":"not-a-number"}}`, `["t1"]`)
	f.Fuzz(func(t *testing.T, rid uint64, fullUpdate bool, torrentsJSON string, removedJSON string) {
		var torrents map[string]TorrentDelta
		_ = json.Unmarshal([]byte(torrentsJSON), &torrents)
		var removed []string
		_ = json.Unmarshal([]byte(removedJSON), &removed)
		previous := Snapshot{RID: 0, Torrents: map[string]*Torrent{}}
		result, err := Merge(previous, Delta{RID: rid, FullUpdate: fullUpdate, Torrents: torrents, Removed: removed})
		if err != nil {
			return
		}
		if result.Applied && result.Ignored {
			t.Fatalf("merge result is both applied and ignored: %+v", result)
		}
	})
}
