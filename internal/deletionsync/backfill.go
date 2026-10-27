package deletionsync

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/vertex-pro/vertex-pro/internal/deletionpolicy"
	"github.com/vertex-pro/vertex-pro/internal/syncmerge"
)

// TagWriter adds Vertex-owned tags through the qui proxy.
type TagWriter interface {
	AddTags(context.Context, []string, []string) error
}

// maxTagBatch matches proxy.MaxTagHashes.
const maxTagBatch = 200

// Backfill adds ownership tags to torrents delivered before delivery-time
// tagging existed. It only considers attempts whose Add effect Vertex
// observed (HASH_SEEN / RECONCILED_CONFIRMED, which the schema ties to
// OBSERVED_EFFECT). ALREADY_PRESENT attempts are excluded: that torrent
// existed before Vertex and may belong to the user.
type Backfill struct {
	DB       *sql.DB
	Instance string
	Tags     TagWriter
	Snapshot func() (syncmerge.Snapshot, bool)
}

// BackfillItem is one torrent missing ownership tags.
type BackfillItem struct {
	Hash    string
	Site    string
	Missing []string
}

// Plan lists torrents Vertex added that are still present on the instance
// and lack an ownership tag. It changes nothing.
func (backfill Backfill) Plan(ctx context.Context) ([]BackfillItem, error) {
	snapshot, ok := backfill.Snapshot()
	if !ok {
		return nil, errors.New("no current snapshot for instance " + backfill.Instance)
	}
	rows, err := backfill.DB.QueryContext(ctx, `SELECT DISTINCT a.qb_torrent_id, j.source_id FROM placement_attempts a JOIN race_jobs j ON j.job_id=a.job_id
		WHERE a.instance_id=? AND a.attempt_state IN ('HASH_SEEN','RECONCILED_CONFIRMED') AND a.side_effect_fact='OBSERVED_EFFECT' AND a.qb_torrent_id IS NOT NULL
		ORDER BY a.qb_torrent_id`, backfill.Instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var plan []BackfillItem
	for rows.Next() {
		var hash, source string
		if err := rows.Scan(&hash, &source); err != nil {
			return nil, err
		}
		hash = strings.ToLower(hash)
		if seen[hash] {
			continue
		}
		seen[hash] = true
		torrent, present := snapshot.Torrents[hash]
		if !present {
			continue
		}
		site, _, _ := strings.Cut(source, "/")
		have := map[string]bool{}
		for _, tag := range strings.Split(torrent.Tags, ",") {
			have[strings.ToLower(strings.TrimSpace(tag))] = true
		}
		var missing []string
		for _, tag := range deletionpolicy.DeliveryTags(nil, site) {
			if !have[strings.ToLower(tag)] {
				missing = append(missing, tag)
			}
		}
		if len(missing) > 0 {
			plan = append(plan, BackfillItem{Hash: hash, Site: site, Missing: missing})
		}
	}
	return plan, rows.Err()
}

// Apply adds the planned tags in batches sharing the same tag set. Adding a
// tag is idempotent, so a partial failure can simply be re-planned.
func (backfill Backfill) Apply(ctx context.Context, plan []BackfillItem) (int, error) {
	groups := map[string][]string{}
	tagsByKey := map[string][]string{}
	for _, item := range plan {
		key := strings.Join(item.Missing, ",")
		groups[key] = append(groups[key], item.Hash)
		tagsByKey[key] = item.Missing
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	applied := 0
	for _, key := range keys {
		hashes := groups[key]
		for start := 0; start < len(hashes); start += maxTagBatch {
			batch := hashes[start:min(start+maxTagBatch, len(hashes))]
			if err := backfill.Tags.AddTags(ctx, batch, tagsByKey[key]); err != nil {
				return applied, err
			}
			applied += len(batch)
		}
	}
	return applied, nil
}
