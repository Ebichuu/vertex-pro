package apipaging

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestCursorRoundTripAndTamperProtection(t *testing.T) {
	cursor := Cursor{SortKey: "2026-08-29T08:00:00Z", ID: "job-1"}
	decoded, err := Decode(Encode(cursor))
	if err != nil || decoded != cursor {
		t.Fatalf("decoded = %+v err = %v", decoded, err)
	}
	encoded := Encode(cursor)
	tampered := "f" + encoded[1:]
	if tampered == encoded {
		tampered = "g" + encoded[1:]
	}
	if _, err := Decode(tampered); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered cursor error = %v", err)
	}
	for _, garbage := range []string{"", "not-a-cursor", "AB"} {
		if _, err := Decode(garbage); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("garbage %q error = %v", garbage, err)
		}
	}
}

func TestCheckRevisionMapsToConflict(t *testing.T) {
	if err := CheckRevision(4, 4); err != nil {
		t.Fatal(err)
	}
	if err := CheckRevision(4, 5); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("error = %v", err)
	}
}

// TestPaginationSurvivesDeletionBetweenPages 验证翻页间隙删除条目后，
// 键元组 cursor 仍能稳定续页：不重复、不跳漏、不崩溃。
func TestPaginationSurvivesDeletionBetweenPages(t *testing.T) {
	database := openMigrated(t)
	seedJobs(t, database, 10)

	fetch := func(cursor string) ([]string, string) {
		query := `SELECT created_at, job_id FROM race_jobs`
		args := []any{}
		if cursor != "" {
			decoded, err := Decode(cursor)
			if err != nil {
				t.Fatal(err)
			}
			query += ` WHERE (created_at, job_id) > (?, ?)`
			args = append(args, decoded.SortKey, decoded.ID)
		}
		query += ` ORDER BY created_at, job_id LIMIT 4`
		rows, err := database.Query(query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		ids := make([]string, 0, 4)
		var lastCreated, lastID string
		for rows.Next() {
			var created, id string
			if err := rows.Scan(&created, &id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
			lastCreated, lastID = created, id
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		next := ""
		if len(ids) == 4 {
			next = Encode(Cursor{SortKey: lastCreated, ID: lastID})
		}
		return ids, next
	}

	page1, cursor := fetch("")
	if len(page1) != 4 {
		t.Fatalf("page1 = %v", page1)
	}
	// 翻页间隙删除第 2、3 页将覆盖的两条记录。
	if _, err := database.Exec(`DELETE FROM race_jobs WHERE job_id IN ('job-5','job-7')`); err != nil {
		t.Fatal(err)
	}
	page2, cursor := fetch(cursor)
	page3, _ := fetch(cursor)
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, page1...), page2...) {
		seen[id] = true
	}
	for _, id := range page3 {
		if seen[id] {
			t.Fatalf("duplicate id across pages: %s", id)
		}
		seen[id] = true
	}
	for _, deleted := range []string{"job-5", "job-7"} {
		if seen[deleted] {
			t.Fatalf("deleted job %s reappeared", deleted)
		}
	}
	total := len(page1) + len(page2) + len(page3)
	if total != 8 {
		t.Fatalf("paged %d of 8 surviving jobs", total)
	}
}

func seedJobs(t *testing.T, database *sql.DB, count int) {
	t.Helper()
	for index := 1; index <= count; index++ {
		id := "job-" + string(rune('0'+index))
		if _, err := database.Exec(`INSERT INTO race_jobs(job_id, source_id, identity_key, job_state, rule_state, torrent_state, placement_state, participation_deadline, observation_deadline, created_at, updated_at) VALUES (?, 'src-1', 'identity-1', 'READY', 'MATCHED', 'READY', 'PREPARED', '2026-08-29T08:00:00Z', '2026-08-29T08:00:00Z', ?, ?)`,
			id, "2026-08-29T08:00:0"+string(rune('0'+index-1))+"Z", "2026-08-29T08:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "api-paging.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}
