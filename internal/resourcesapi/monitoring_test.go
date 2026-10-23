package resourcesapi

import (
	"net/http/httptest"
	"testing"
)

func TestDashboardUsesMetadataAndLeavesMissingRatesUnknown(t *testing.T) {
	database := openMigrated(t)
	seedResourceFixture(t, database)
	if _, err := database.Exec(`INSERT INTO instance_metadata(instance_id,name,enabled,updated_at) VALUES('qb-1','Actual Downloader',1,'2026-10-22T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	cards, err := (Deps{Database: database}).instanceCards(httptest.NewRequest("GET", "/api/v1/dashboard", nil))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, card := range cards {
		if card["id"] != "qb-1" {
			continue
		}
		found = true
		if card["name"] != "Actual Downloader" {
			t.Fatalf("name=%v", card["name"])
		}
		for _, key := range []string{"download_rate", "upload_rate", "downloading_count", "seeding_count"} {
			if card[key] != nil {
				t.Fatalf("missing %s=%v", key, card[key])
			}
		}
	}
	if !found {
		t.Fatal("missing qb-1 card")
	}
}
