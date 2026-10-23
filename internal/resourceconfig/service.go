// Package resourceconfig owns configuration mutations, preserving historical
// foreign keys when an operator archives a resource.
package resourceconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vertex-pro/vertex-pro/internal/db"
	"github.com/vertex-pro/vertex-pro/internal/sitecredentials"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid configuration")
var ErrConflict = errors.New("resource revision conflict")
var ErrNotFound = errors.New("resource not found")
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Service struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s Service) UpdateSite(ctx context.Context, id, expected string, enabled *bool, credential *string) (string, error) {
	if !idPattern.MatchString(id) || expected == "" || (enabled == nil && credential == nil) {
		return "", fmt.Errorf("%w: site update", ErrInvalid)
	}
	if credential != nil {
		if err := sitecredentials.Validate(*credential); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	stamp := NextTimestamp(s.Now(), expected)
	result, err := s.DB.ExecContext(ctx, `UPDATE sites SET enabled=COALESCE(?,enabled),credential_ref=COALESCE(?,credential_ref),updated_at=? WHERE site_id=? AND archived=0 AND updated_at=?`, enabled, credential, stamp, id, expected)
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrConflict
	}
	return stamp, nil
}
func (s Service) Archive(ctx context.Context, kind, id, expected string) error {
	table, key, revision := "", "", "updated_at"
	switch kind {
	case "sites":
		table, key = "sites", "site_id"
	case "feeds":
		table, key = "feeds", "feed_id"
	case "rules":
		table, key, revision = "rules", "rule_id", "revision"
	default:
		return ErrNotFound
	}
	if !idPattern.MatchString(id) || expected == "" {
		return fmt.Errorf("%w: resource revision", ErrInvalid)
	}
	return db.RunImmediate(ctx, s.DB, func(ctx context.Context, tx *db.ImmediateTx) error {
		stamp := s.Now().UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE `+table+` SET archived=1,enabled=0,updated_at=? WHERE `+key+`=? AND archived=0 AND CAST(`+revision+` AS TEXT)=?`, stamp, id, expected)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		if kind == "sites" {
			if _, err := tx.ExecContext(ctx, `UPDATE feeds SET archived=1,enabled=0,updated_at=? WHERE site_id=?`, stamp, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE rules SET archived=1,enabled=0,updated_at=? WHERE site_id=?`, stamp, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// NextTimestamp keeps timestamp ETags distinct even when the clock has not advanced.
func NextTimestamp(now time.Time, previous string) string {
	current := now.UTC()
	if before, err := time.Parse(time.RFC3339Nano, previous); err == nil && !current.After(before) {
		current = before.Add(time.Nanosecond)
	}
	return current.Format(time.RFC3339Nano)
}

type Group struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Members  []string `json:"members"`
	Revision int      `json:"revision"`
}

func (s Service) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT group_id,name,members_json,revision FROM instance_groups ORDER BY group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Group{}
	for rows.Next() {
		var g Group
		var raw string
		if err := rows.Scan(&g.ID, &g.Name, &raw, &g.Revision); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &g.Members); err != nil {
			return nil, err
		}
		result = append(result, g)
	}
	return result, rows.Err()
}
func (s Service) SaveGroup(ctx context.Context, g Group, expected int) (Group, error) {
	if !idPattern.MatchString(g.ID) || strings.TrimSpace(g.Name) == "" || len(g.Name) > 200 || len(g.Members) == 0 || len(g.Members) > 64 || expected < 0 {
		return g, fmt.Errorf("invalid group")
	}
	seen := map[string]bool{}
	for _, id := range g.Members {
		if !idPattern.MatchString(id) || seen[id] {
			return g, fmt.Errorf("invalid group members")
		}
		seen[id] = true
	}
	raw, _ := json.Marshal(g.Members)
	stamp := s.Now().UTC().Format(time.RFC3339Nano)
	err := db.RunImmediate(ctx, s.DB, func(ctx context.Context, tx *db.ImmediateTx) error {
		for _, id := range g.Members {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance_metadata WHERE instance_id=?`, id).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("%w: unknown group member", ErrInvalid)
			}
		}
		var result sql.Result
		var err error
		if expected == 0 {
			result, err = tx.ExecContext(ctx, `INSERT INTO instance_groups(group_id,name,members_json,revision,updated_at) VALUES(?,?,?,1,?) ON CONFLICT(group_id) DO NOTHING`, g.ID, g.Name, string(raw), stamp)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE instance_groups SET name=?,members_json=?,revision=revision+1,updated_at=? WHERE group_id=? AND revision=?`, g.Name, string(raw), stamp, g.ID, expected)
		}
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		return nil
	})
	g.Revision = expected + 1
	return g, err
}
func ParseExpected(raw string) (string, error) {
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", fmt.Errorf("invalid revision")
	}
	v, err := strconv.Unquote(raw)
	if err != nil || v == "" {
		return "", fmt.Errorf("invalid revision")
	}
	return v, nil
}
