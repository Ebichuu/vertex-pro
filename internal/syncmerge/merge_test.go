package syncmerge

import "testing"

func TestMergePreservesAndReplacesServerCapacityFacts(t *testing.T) {
	full, err := Merge(Snapshot{}, Delta{RID: 1, FullUpdate: true, Torrents: map[string]TorrentDelta{}, ServerState: map[string]any{"free_space_on_disk": 100, "connection_status": "connected"}})
	if err != nil {
		t.Fatal(err)
	}
	delta, err := Merge(full.Snapshot, Delta{RID: 2, ServerState: map[string]any{"free_space_on_disk": 50}})
	if err != nil {
		t.Fatal(err)
	}
	if delta.Snapshot.ServerState["free_space_on_disk"] != 50 || delta.Snapshot.ServerState["connection_status"] != "connected" || full.Snapshot.ServerState["free_space_on_disk"] != 100 {
		t.Fatal("incremental server state did not preserve immutable snapshots")
	}
	replaced, err := Merge(delta.Snapshot, Delta{RID: 3, FullUpdate: true, Torrents: map[string]TorrentDelta{}})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Snapshot.ServerState["free_space_on_disk"] != nil {
		t.Fatal("full snapshot inherited old capacity")
	}
}

func TestMergeBuildsBaselineAndInheritsIncrementalFields(t *testing.T) {
	baseline, err := Merge(Snapshot{}, Delta{RID: 0, FullUpdate: true, Torrents: mustDeltas(t, map[string]map[string]any{"hash-1": {"name": "one", "amount_left": 100, "state": "downloading"}})})
	if err != nil {
		t.Fatalf("baseline error = %v", err)
	}
	torrent := baseline.Snapshot.Torrents["hash-1"]
	if torrent.AmountLeftState != "KNOWN" || torrent.AmountLeft == nil || *torrent.AmountLeft != 100 {
		t.Fatalf("baseline torrent = %+v", torrent)
	}
	update, err := Merge(baseline.Snapshot, Delta{RID: 1, Torrents: mustDeltas(t, map[string]map[string]any{"hash-1": {"state": "uploading"}})})
	if err != nil {
		t.Fatalf("increment error = %v", err)
	}
	torrent = update.Snapshot.Torrents["hash-1"]
	if torrent.Name != "one" || torrent.State != "uploading" || torrent.AmountLeftState != "KNOWN" {
		t.Fatalf("inherited torrent = %+v", torrent)
	}
}

func TestMergeHandlesDeletionDuplicateAndOutOfOrderDeltas(t *testing.T) {
	baseline, err := Merge(Snapshot{}, Delta{RID: 0, Torrents: mustDeltas(t, map[string]map[string]any{"hash-1": {"name": "one"}, "hash-2": {"name": "two"}})})
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Merge(baseline.Snapshot, Delta{RID: 2, Removed: []string{"hash-2"}})
	if _, kept := applied.Snapshot.Torrents["hash-2"]; err != nil || !applied.Applied || kept {
		t.Fatalf("deletion = %+v, %v", applied, err)
	}
	duplicate, err := Merge(applied.Snapshot, Delta{RID: 2, Torrents: mustDeltas(t, map[string]map[string]any{"hash-1": {"state": "wrong"}})})
	if err != nil || !duplicate.Ignored || duplicate.Snapshot.Torrents["hash-1"].Has(FieldState) {
		t.Fatalf("duplicate = %+v, %v", duplicate, err)
	}
	stale, err := Merge(applied.Snapshot, Delta{RID: 1, Torrents: mustDeltas(t, map[string]map[string]any{"hash-1": {"state": "stale"}})})
	if err != nil || !stale.Ignored || stale.Snapshot.Torrents["hash-1"].Has(FieldState) {
		t.Fatalf("stale = %+v, %v", stale, err)
	}
}

func TestMergeMarksInvalidAmountLeftUnknownInsteadOfZero(t *testing.T) {
	for _, value := range []any{nil, -1, "not-a-number", true, 1.5} {
		result, err := Merge(Snapshot{}, Delta{RID: 0, Torrents: mustDeltas(t, map[string]map[string]any{"hash-1": {"amount_left": value}})})
		if err != nil {
			t.Fatalf("value %v error = %v", value, err)
		}
		torrent := result.Snapshot.Torrents["hash-1"]
		if torrent.AmountLeftState != "UNKNOWN" || torrent.AmountLeft != nil {
			t.Fatalf("value %v torrent = %+v", value, torrent)
		}
	}
}

func TestMergeRequiresBaselineForIncrementalAndRejectsEmptyID(t *testing.T) {
	if _, err := Merge(Snapshot{}, Delta{RID: 1}); err == nil {
		t.Fatal("missing baseline error = nil")
	}
	if _, err := Merge(Snapshot{}, Delta{RID: 0, Torrents: mustDeltas(t, map[string]map[string]any{"": {"name": "bad"}})}); err == nil {
		t.Fatal("empty ID error = nil")
	}
}

func mustDeltas(t *testing.T, raw map[string]map[string]any) map[string]TorrentDelta {
	t.Helper()
	torrents, err := TorrentDeltas(raw)
	if err != nil {
		t.Fatal(err)
	}
	return torrents
}
