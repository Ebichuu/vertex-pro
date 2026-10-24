package settings

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func openSettings(t *testing.T, path string) (*Service, *sql.DB) {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: path, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	service, err := New(database, func() time.Time { return time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return service, database
}

func TestDefaultsAndPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	service, database := openSettings(t, path)
	initial, err := service.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if initial.Timezone != "UTC" || initial.RSSPollIntervalMinutes != 60 || initial.RateLimitPerMinute != 30 || initial.RequestBurst != 4 || initial.MaxConcurrentPreparations != 8 || initial.MaxPreparationsPerHost != 2 || initial.MaxConcurrentRSSFetches != 4 || initial.SessionTTLHours != 24 || initial.StatsRawRetentionDays != 30 || initial.StatsHourlyRetentionDays != 180 || initial.Revision != 1 {
		t.Fatalf("defaults = %+v", initial)
	}
	values := initial.Values
	values.Timezone = " Asia/Shanghai "
	values.SessionTTLHours = 12
	values.RequestBurst, values.MaxConcurrentPreparations, values.MaxPreparationsPerHost = 6, 12, 3
	saved, err := service.Update(context.Background(), initial.Revision, values)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Timezone != "Asia/Shanghai" || saved.Revision != 2 || saved.UpdatedAt != "2026-09-06T08:00:00Z" {
		t.Fatalf("saved = %+v", saved)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	service, database = openSettings(t, path)
	defer database.Close()
	reloaded, err := service.Load(context.Background())
	if err != nil || reloaded != saved {
		t.Fatalf("reopen = %+v, %v", reloaded, err)
	}
}

func TestValidationDoesNotChangeStoredSettings(t *testing.T) {
	service, database := openSettings(t, filepath.Join(t.TempDir(), "settings.db"))
	defer database.Close()
	initial, err := service.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Values){
		"blank zone":            func(v *Values) { v.Timezone = " " },
		"local zone":            func(v *Values) { v.Timezone = "Local" },
		"invalid zone":          func(v *Values) { v.Timezone = "Mars/Olympus" },
		"poll zero":             func(v *Values) { v.RSSPollIntervalMinutes = 0 },
		"poll high":             func(v *Values) { v.RSSPollIntervalMinutes = 1441 },
		"rate high":             func(v *Values) { v.RateLimitPerMinute = 601 },
		"concurrency high":      func(v *Values) { v.MaxConcurrentRSSFetches = 33 },
		"burst high":            func(v *Values) { v.RequestBurst = 17 },
		"preparation zero":      func(v *Values) { v.MaxConcurrentPreparations = 0 },
		"site preparation high": func(v *Values) { v.MaxPreparationsPerHost = 9 },
		"ttl high":              func(v *Values) { v.SessionTTLHours = 721 },
		"raw negative":          func(v *Values) { v.StatsRawRetentionDays = -1 },
		"hourly high":           func(v *Values) { v.StatsHourlyRetentionDays = 3651 },
		"retention order":       func(v *Values) { v.StatsHourlyRetentionDays = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			value := initial.Values
			change(&value)
			if _, err := service.Update(context.Background(), initial.Revision, value); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid input: %v", err)
			}
		})
	}
	current, err := service.Load(context.Background())
	if err != nil || current != initial {
		t.Fatalf("invalid update changed row: %+v, %v", current, err)
	}
	if _, err := New(nil, nil); err == nil {
		t.Fatal("nil database accepted")
	}
}

func TestConcurrentUpdatesHaveExactlyOneWinner(t *testing.T) {
	service, database := openSettings(t, filepath.Join(t.TempDir(), "settings.db"))
	defer database.Close()
	initial, err := service.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		settings Settings
		err      error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for _, zone := range []string{"Asia/Shanghai", "Europe/London"} {
		go func() {
			<-start
			value := initial.Values
			value.Timezone = zone
			saved, err := service.Update(context.Background(), initial.Revision, value)
			results <- outcome{saved, err}
		}()
	}
	close(start)
	var winner Settings
	conflicts := 0
	for range 2 {
		result := <-results
		if errors.Is(result.err, ErrConflict) {
			conflicts++
			continue
		}
		if result.err != nil {
			t.Fatal(result.err)
		}
		winner = result.settings
	}
	current, err := service.Load(context.Background())
	if err != nil || conflicts != 1 || current != winner || current.Revision != 2 {
		t.Fatalf("conflicts=%d, winner=%+v, current=%+v, error=%v", conflicts, winner, current, err)
	}
}
