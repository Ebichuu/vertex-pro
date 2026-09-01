package torrentidentity

import (
	"strings"
	"testing"

	"github.com/vertex-pro/vertex-pro/internal/torrentparse"
)

func TestIdentityCalculatesV1FromInfo(t *testing.T) {
	info := infoValue(
		entry("length", integer(1)),
		entry("name", stringValue("test")),
		entry("pieces", stringValue("12345678901234567890")),
	)
	identity, err := FromInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Version != VersionV1 || len(identity.V1) != 40 || identity.V2 != "" {
		t.Fatalf("identity=%+v", identity)
	}
}

func TestIdentityCalculatesV2AndHybridAsStrongTypes(t *testing.T) {
	v2 := infoValue(entry("file tree", torrentparse.Value{Kind: 'd'}), entry("length", integer(1)), entry("meta version", integer(2)), entry("name", stringValue("test")))
	identity, err := FromInfo(v2)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Version != VersionV2 || len(identity.V2) != 64 || identity.V1 != "" {
		t.Fatalf("v2=%+v", identity)
	}

	hybrid := infoValue(entry("file tree", torrentparse.Value{Kind: 'd'}), entry("length", integer(1)), entry("meta version", integer(2)), entry("name", stringValue("test")), entry("pieces", stringValue("12345678901234567890")))
	identity, err = FromInfo(hybrid)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Version != VersionHybrid || len(identity.V1) != 40 || len(identity.V2) != 64 {
		t.Fatalf("hybrid=%+v", identity)
	}
}

func TestIdentityRejectsMissingOrMalformedVersionFields(t *testing.T) {
	tests := []torrentparse.Value{
		infoValue(entry("length", integer(1)), entry("name", stringValue("test"))),
		infoValue(entry("name", stringValue("test")), entry("pieces", stringValue("x"))),
		infoValue(entry("length", integer(1)), entry("meta version", integer(1)), entry("name", stringValue("test"))),
		infoValue(entry("length", integer(1)), entry("meta version", stringValue("two")), entry("name", stringValue("test"))),
	}
	for index, info := range tests {
		t.Run(strings.TrimSpace(string(rune('a'+index))), func(t *testing.T) {
			if _, err := FromInfo(info); err == nil {
				t.Fatal("expected identity error")
			}
		})
	}
}

func TestIdentityRejectsUnsortedInfoDictionary(t *testing.T) {
	info := torrentparse.Value{Kind: 'd', Dict: []torrentparse.DictEntry{{Key: []byte("z"), Value: stringValue("x")}, {Key: []byte("a"), Value: stringValue("y")}}}
	if _, err := FromInfo(info); err == nil {
		t.Fatal("expected ordering error")
	}
}

func entry(key string, value torrentparse.Value) torrentparse.DictEntry {
	return torrentparse.DictEntry{Key: []byte(key), Value: value}
}
func stringValue(value string) torrentparse.Value {
	return torrentparse.Value{Kind: 's', Bytes: []byte(value)}
}
func integer(value int64) torrentparse.Value { return torrentparse.Value{Kind: 'i', Int: value} }
func infoValue(entries ...torrentparse.DictEntry) torrentparse.Value {
	return torrentparse.Value{Kind: 'd', Dict: entries}
}
