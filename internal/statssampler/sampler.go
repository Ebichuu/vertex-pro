package statssampler

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Source 标记样本来自哪个 qB/qui 数据源。合同要求使用哪个来源必须随 API 返回。
type Source string

const (
	SourceSyncServerState Source = "SYNC_SERVER_STATE"
	SourceTransferInfo    Source = "TRANSFER_INFO"
)

// 字段名与 qB WebAPI 冻结一致。
const (
	FieldDownloaded = "dl_info_data"
	FieldUploaded   = "up_info_data"
	FieldDlRate     = "dl_info_speed"
	FieldUpRate     = "up_info_speed"
)

// PayloadKind 标识上游响应类型。
type PayloadKind string

const (
	KindSyncMaindata PayloadKind = "SYNC_MAINDATA"
	KindTransferInfo PayloadKind = "TRANSFER_INFO"
)

// RawPayload 是一次上游响应的规范化载体。
type RawPayload struct {
	Kind PayloadKind
	Body map[string]any
}

// Sample 是一次成功解析的计数与速率快照。
type Sample struct {
	InstanceID  string
	SampledAt   string
	Downloaded  int64
	Uploaded    int64
	DlRate      int64
	UpRate      int64
	Source      Source
	Missing     []string
	HasCounters bool
	HasRates    bool
}

var counterFields = []string{FieldDownloaded, FieldUploaded}
var rateFields = []string{FieldDlRate, FieldUpRate}

// SampleFromPayloads 按优先级解析：sync server_state 优先，transfer-info
// 兜底。任一 payload 中某字段缺失、类型错误或为负数即视为该字段缺失并
// 记入 Missing；计数缺失时 HasCounters=false（STAT-02 只建基线，不产生
// 增量），速率缺失时 HasRates=false（速率不可用不得补造）。
func SampleFromPayloads(instanceID string, sampledAt string, payloads []RawPayload) (Sample, error) {
	if strings.TrimSpace(instanceID) == "" {
		return Sample{}, errors.New("instance id is required")
	}
	if strings.TrimSpace(sampledAt) == "" {
		return Sample{}, errors.New("sampled at is required")
	}
	ordered := orderPayloads(payloads)
	var lastErr error
	for _, payload := range ordered {
		source, body, ok := extract(payload)
		if !ok {
			lastErr = fmt.Errorf("payload kind %s has no usable stats body", payload.Kind)
			continue
		}
		sample := Sample{InstanceID: instanceID, SampledAt: sampledAt, Source: source}
		downloaded := readCounter(body, FieldDownloaded, &sample.Missing)
		uploaded := readCounter(body, FieldUploaded, &sample.Missing)
		sample.DlRate, sample.UpRate = readRate(body, FieldDlRate, &sample.Missing), readRate(body, FieldUpRate, &sample.Missing)
		sample.Downloaded, sample.Uploaded = downloaded, uploaded
		sample.HasCounters = !contains(sample.Missing, FieldDownloaded) && !contains(sample.Missing, FieldUploaded)
		sample.HasRates = !contains(sample.Missing, FieldDlRate) && !contains(sample.Missing, FieldUpRate)
		// 任一字段可解析即采用该来源并标记缺失字段；四个字段全缺才降级下一来源。
		if len(sample.Missing) < len(counterFields)+len(rateFields) {
			sort.Strings(sample.Missing)
			return sample, nil
		}
		lastErr = fmt.Errorf("payload kind %s lacks counters and rates", payload.Kind)
	}
	if lastErr != nil {
		return Sample{}, fmt.Errorf("no usable stats source: %w", lastErr)
	}
	return Sample{}, errors.New("no stats payload provided")
}

// orderPayloads 固定来源优先级：sync server_state 在前，transfer-info 兜底。
func orderPayloads(payloads []RawPayload) []RawPayload {
	ordered := make([]RawPayload, 0, len(payloads))
	for _, payload := range payloads {
		if payload.Kind == KindSyncMaindata {
			ordered = append(ordered, payload)
		}
	}
	for _, payload := range payloads {
		if payload.Kind == KindTransferInfo {
			ordered = append(ordered, payload)
		}
	}
	return ordered
}

func extract(payload RawPayload) (Source, map[string]any, bool) {
	switch payload.Kind {
	case KindSyncMaindata:
		body, ok := payload.Body["server_state"].(map[string]any)
		if !ok {
			return SourceSyncServerState, nil, false
		}
		return SourceSyncServerState, body, true
	case KindTransferInfo:
		if payload.Body == nil {
			return SourceTransferInfo, nil, false
		}
		return SourceTransferInfo, payload.Body, true
	}
	return "", nil, false
}

func readCounter(body map[string]any, field string, missing *[]string) int64 {
	value, ok := readNonNegativeNumber(body, field)
	if !ok {
		*missing = append(*missing, field)
		return 0
	}
	return value
}

func readRate(body map[string]any, field string, missing *[]string) int64 {
	value, ok := readNonNegativeNumber(body, field)
	if !ok {
		*missing = append(*missing, field)
		return 0
	}
	return value
}

func readNonNegativeNumber(body map[string]any, field string) (int64, bool) {
	raw, present := body[field]
	if !present || raw == nil {
		return 0, false
	}
	switch value := raw.(type) {
	case float64:
		if value < 0 || value != value || value > 1e19 {
			return 0, false
		}
		return int64(value), true
	case int64:
		if value < 0 {
			return 0, false
		}
		return value, true
	case int:
		if value < 0 {
			return 0, false
		}
		return int64(value), true
	}
	return 0, false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
