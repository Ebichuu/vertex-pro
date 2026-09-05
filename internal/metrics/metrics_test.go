package metrics

import (
	"strings"
	"testing"
)

func TestRenderIsSortedAndDeterministic(t *testing.T) {
	registry := New()
	registry.Add("vertex_sync_ticks_total", map[string]string{"result": "stale"}, 2)
	registry.Inc("vertex_sync_ticks_total", map[string]string{"result": "ok"})
	registry.Inc("vertex_add_attempts_total", nil)
	first := registry.Render()
	second := registry.Render()
	if first != second {
		t.Fatalf("render is not deterministic:\n%s\n---\n%s", first, second)
	}
	expected := "# TYPE vertex_add_attempts_total counter\nvertex_add_attempts_total 1\n" +
		"# TYPE vertex_sync_ticks_total counter\n" +
		`vertex_sync_ticks_total{result="ok"} 1` + "\n" +
		`vertex_sync_ticks_total{result="stale"} 2` + "\n"
	if first != expected {
		t.Fatalf("render=%s want=%s", first, expected)
	}
}

func TestLabelsAreEscapedAndSorted(t *testing.T) {
	registry := New()
	registry.Inc("vertex_test_total", map[string]string{"b": "2", "a": `x"y`})
	rendered := registry.Render()
	if !strings.Contains(rendered, `vertex_test_total{a="x\"y",b="2"} 1`) {
		t.Fatalf("rendered=%s", rendered)
	}
}
