package statssampler

import (
	"reflect"
	"testing"
)

func TestServerStatePreferredOverTransferInfo(t *testing.T) {
	result, err := SampleFromPayloads("qb-1", "2026-08-29T08:00:00Z", []RawPayload{
		{Kind: KindTransferInfo, Body: map[string]any{"dl_info_data": float64(1), "up_info_data": float64(2), "dl_info_speed": float64(3), "up_info_speed": float64(4)}},
		{Kind: KindSyncMaindata, Body: map[string]any{"server_state": map[string]any{"dl_info_data": float64(100), "up_info_data": float64(200), "dl_info_speed": float64(10), "up_info_speed": float64(20)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != SourceSyncServerState || result.Downloaded != 100 || result.Uploaded != 200 || result.DlRate != 10 || result.UpRate != 20 {
		t.Fatalf("result = %+v", result)
	}
	if !result.HasCounters || !result.HasRates || len(result.Missing) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestTransferInfoFallbackWhenSyncLacksServerState(t *testing.T) {
	result, err := SampleFromPayloads("qb-1", "2026-08-29T08:00:00Z", []RawPayload{
		{Kind: KindSyncMaindata, Body: map[string]any{"rid": float64(7)}},
		{Kind: KindTransferInfo, Body: map[string]any{"dl_info_data": float64(5), "up_info_data": float64(6)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != SourceTransferInfo || result.Downloaded != 5 || result.Uploaded != 6 {
		t.Fatalf("result = %+v", result)
	}
	if !result.HasCounters {
		t.Fatalf("counters must be usable: %+v", result)
	}
	if !reflect.DeepEqual(result.Missing, []string{"dl_info_speed", "up_info_speed"}) {
		t.Fatalf("missing = %v", result.Missing)
	}
	if result.HasRates {
		t.Fatal("missing rates must be flagged")
	}
}

func TestMissingAndInvalidFieldsAreReported(t *testing.T) {
	result, err := SampleFromPayloads("qb-1", "2026-08-29T08:00:00Z", []RawPayload{
		{Kind: KindSyncMaindata, Body: map[string]any{"server_state": map[string]any{
			"dl_info_data": "not-a-number", "up_info_data": nil, "dl_info_speed": float64(-5), "up_info_speed": float64(3),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.HasCounters {
		t.Fatal("missing counters must not be usable")
	}
	if !reflect.DeepEqual(result.Missing, []string{"dl_info_data", "dl_info_speed", "up_info_data"}) {
		t.Fatalf("missing = %v", result.Missing)
	}
	if result.UpRate != 3 {
		t.Fatalf("valid rate lost: %+v", result)
	}
}

func TestNoUsableSourceErrors(t *testing.T) {
	if _, err := SampleFromPayloads("qb-1", "2026-08-29T08:00:00Z", nil); err == nil {
		t.Fatal("no payload error = nil")
	}
	if _, err := SampleFromPayloads("", "x", nil); err == nil {
		t.Fatal("no instance error = nil")
	}
}
