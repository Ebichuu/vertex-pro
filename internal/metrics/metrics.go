// Package metrics provides the minimal in-process counter registry backing
// the /metrics endpoint. Counters are monotonically increasing, labeled, and
// rendered in the Prometheus text exposition format; no third-party
// dependencies and no allocation beyond the label values.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry is a set of monotonically increasing counters.
type Registry struct {
	mu       sync.Mutex
	counters map[string]map[string]uint64 // name -> labelset -> value
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{counters: make(map[string]map[string]uint64)}
}

// Inc adds one to the counter with the given name and sorted labelset.
func (registry *Registry) Inc(name string, labels map[string]string) {
	registry.Add(name, labels, 1)
}

// Add adds delta to the counter with the given name and labelset.
func (registry *Registry) Add(name string, labels map[string]string, delta uint64) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	labelset := renderLabels(labels)
	values, ok := registry.counters[name]
	if !ok {
		values = make(map[string]uint64)
		registry.counters[name] = values
	}
	values[labelset] += delta
}

// Render writes every counter in the Prometheus text exposition format:
// counter families sorted by name, labelsets sorted within a family.
func (registry *Registry) Render() string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	names := make([]string, 0, len(registry.counters))
	for name := range registry.counters {
		names = append(names, name)
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		values := registry.counters[name]
		labelsets := make([]string, 0, len(values))
		for labelset := range values {
			labelsets = append(labelsets, labelset)
		}
		sort.Strings(labelsets)
		fmt.Fprintf(&builder, "# TYPE %s counter\n", name)
		for _, labelset := range labelsets {
			fmt.Fprintf(&builder, "%s%s %d\n", name, labelset, values[labelset])
		}
	}
	return builder.String()
}

func renderLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%q", key, labels[key]))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}
