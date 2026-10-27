package syncmerge

import "testing"

// Incremental merges share unchanged torrents with the previous snapshot but
// must never modify anything the previous snapshot can observe.
func TestIncrementalMergeLeavesPreviousSnapshotUnchanged(t *testing.T) {
	base, err := Merge(Snapshot{}, Delta{RID: 1, FullUpdate: true, Torrents: mustDeltas(t, map[string]map[string]any{
		"a": {"state": "stalledUP", "amount_left": float64(0)},
		"b": {"state": "downloading", "amount_left": float64(10)},
	}), ServerState: map[string]any{"free_space_on_disk": float64(100)}})
	if err != nil {
		t.Fatal(err)
	}
	previous := base.Snapshot
	next, err := Merge(previous, Delta{RID: 2, Torrents: mustDeltas(t, map[string]map[string]any{"b": {"state": "uploading", "amount_left": float64(0)}}), Removed: []string{"a"}, ServerState: map[string]any{"free_space_on_disk": float64(50)}})
	if err != nil {
		t.Fatal(err)
	}
	if previous.RID != 1 || len(previous.Torrents) != 2 || previous.Torrents["b"].State != "downloading" || *previous.Torrents["b"].AmountLeft != 10 || previous.ServerState["free_space_on_disk"] != float64(100) {
		t.Fatalf("previous snapshot mutated: %+v", previous)
	}
	if _, ok := next.Snapshot.Torrents["a"]; ok || next.Snapshot.Torrents["b"].State != "uploading" || *next.Snapshot.Torrents["b"].AmountLeft != 0 || next.Snapshot.ServerState["free_space_on_disk"] != float64(50) {
		t.Fatalf("next snapshot = %+v", next.Snapshot)
	}
	ignored, err := Merge(next.Snapshot, Delta{RID: 2})
	if err != nil || !ignored.Ignored || ignored.Snapshot.RID != 2 {
		t.Fatalf("duplicate RID = %+v, %v", ignored, err)
	}
}

// The typed decoding keeps the former map semantics: unreported fields stay
// unknown, non-integral counters are invalid, and an explicit null amount_left
// downgrades a previously known liability to UNKNOWN.
func TestTypedFieldsTrackPresenceAndValidity(t *testing.T) {
	base, err := Merge(Snapshot{}, Delta{RID: 1, FullUpdate: true, Torrents: mustDeltas(t, map[string]map[string]any{
		"a": {"name": "A", "uploaded": float64(1 << 40), "downloaded": 1.5, "ratio": 2.25, "amount_left": "42", "num_seeds": 3, "unknown_field": "ignored"},
	})})
	if err != nil {
		t.Fatal(err)
	}
	a := base.Snapshot.Torrents["a"]
	if a.Name != "A" || a.Uploaded != 1<<40 || !a.Has(FieldUploaded) || a.Has(FieldDownloaded) || a.Ratio != 2.25 || a.NumSeeds != 3 || a.Has(FieldState) || *a.AmountLeft != 42 {
		t.Fatalf("typed torrent = %+v", a)
	}
	if value, ok := a.Value("uploaded"); !ok || value != int64(1<<40) {
		t.Fatalf("Value(uploaded) = %v, %v", value, ok)
	}
	if _, ok := a.Value("state"); ok {
		t.Fatal("unreported field returned a value")
	}
	next, err := Merge(base.Snapshot, Delta{RID: 2, Torrents: mustDeltas(t, map[string]map[string]any{"a": {"amount_left": nil, "state": "uploading"}})})
	if err != nil {
		t.Fatal(err)
	}
	a = next.Snapshot.Torrents["a"]
	if a.AmountLeftState != "UNKNOWN" || a.AmountLeft != nil || a.State != "uploading" || a.Name != "A" || !a.Has(FieldUploaded) {
		t.Fatalf("after null amount_left = %+v", a)
	}
}
