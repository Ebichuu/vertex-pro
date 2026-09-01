package contracts

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUserStatusMappingContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "contracts", "user_status_mapping.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user status mapping: %v", err)
	}
	content := string(data)
	if strings.HasPrefix(content, "\ufeff") {
		t.Fatal("user status mapping must be UTF-8 without BOM")
	}
	for _, label := range []string{"等待规则", "准备种子", "等待下载器", "正在添加", "下载中", "做种中", "已完成", "已删除", "失败", "需确认"} {
		if !strings.Contains(content, label) {
			t.Errorf("missing visible label %q", label)
		}
	}
	for _, required := range []string{
		"precedence:",
		"NEEDS_CONFIRMATION",
		"job_state=UNKNOWN",
		"job_state=EXPIRED_UNKNOWN",
		"forbidden_success_mapping: [UNKNOWN, EXPIRED_UNKNOWN, CAPACITY_UNCERTAIN, SNAPSHOT_STALE, STORAGE_UNVERIFIED]",
		"hidden_internal_terms: [Candidate, Placement, Reservation, Reflection, SubmitIntent, capacity_epoch]",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("missing mapping safety rule %q", required)
		}
	}
	for _, internal := range []string{"Candidate", "Placement", "Reservation", "Reflection", "SubmitIntent", "capacity_epoch"} {
		if strings.Contains(content, "visible_labels:\n"+internal) {
			t.Errorf("internal term %q must not be a visible label", internal)
		}
	}
}
