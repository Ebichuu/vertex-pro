package torrentparse

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestParseSingleFileTorrent(t *testing.T) {
	data := []byte("d4:infod6:lengthi42e4:name8:file.bin12:piece lengthi16384eee")
	torrent, err := Parse(data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if torrent.Name != "file.bin" || torrent.TotalSize != 42 || len(torrent.Files) != 1 || torrent.Files[0].Path[0] != "file.bin" {
		t.Fatalf("torrent=%+v", torrent)
	}
}

func TestParseMultiFileTorrentAndLimits(t *testing.T) {
	fileOne := "d6:lengthi2e4:pathl4:dir13:a.eee"
	fileTwo := "d6:lengthi3e4:pathl4:dir23:b.eee"
	data := []byte("d4:infod5:filesl" + fileOne + fileTwo + "e4:name4:rootee")
	torrent, err := Parse(data, Limits{MaxFiles: 2, MaxTotalSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(torrent.Files) != 2 || torrent.TotalSize != 5 {
		t.Fatalf("torrent=%+v", torrent)
	}
	if torrent.Files[0].Path[0] != "dir1" {
		t.Fatalf("files=%+v", torrent.Files)
	}
}

func TestParseRejectsPathTraversalNULAndInvalidShape(t *testing.T) {
	tests := []string{
		"d4:infod4:name2:..6:lengthi1eee",
		"d4:infod4:name5:a/b.c6:lengthi1eee",
		"d4:infod4:name3:a\x00b6:lengthi1eee",
		"d4:infod5:filesld6:lengthi1e4:pathl2:..eee4:name4:rootee",
		"d4:infod4:name4:root6:lengthi-1eee",
		"d4:infod4:name4:root6:lengthi1eeeTRAIL",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := Parse([]byte(input), Limits{}); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestParseRejectsResourceExhaustion(t *testing.T) {
	data := []byte("d4:infod6:lengthi1e4:name4:rootee")
	if _, err := Parse(data, Limits{MaxNodes: 2}); err == nil || !strings.Contains(err.Error(), "node") {
		t.Fatalf("err=%v", err)
	}
	if _, err := Parse(data, Limits{MaxStringLen: 2}); err == nil || !strings.Contains(err.Error(), "string") {
		t.Fatalf("err=%v", err)
	}
	if _, err := Parse(data, Limits{MaxTotalSize: 0}); err != nil {
		t.Fatalf("defaults should apply: %v", err)
	}
}

func TestParseRejectsUnsortedOrDuplicateDictionaryKeys(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("d4:infod4:name4:root6:lengthi1eee3:foo3:bare"),
		[]byte("d4:infod4:name4:root6:lengthi1eee4:name4:rootee"),
	} {
		if _, err := Parse(data, Limits{}); err == nil {
			t.Fatal("expected dictionary ordering error")
		}
	}
}

func TestParseRejectsFileCountAndTotalSizeLimits(t *testing.T) {
	fileOne := "d6:lengthi2e4:pathl1:aee"
	fileTwo := "d6:lengthi3e4:pathl1:bee"
	data := []byte("d4:infod5:filesl" + fileOne + fileTwo + "e4:name4:rootee")
	if _, err := Parse(data, Limits{MaxFiles: 1}); err == nil || !strings.Contains(err.Error(), "file count") {
		t.Fatalf("err=%v", err)
	}
	if _, err := Parse(data, Limits{MaxTotalSize: 4}); err == nil || !strings.Contains(err.Error(), "total size") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseRejectsDeepNesting(t *testing.T) {
	data := []byte(fmt.Sprintf("d4:infod6:lengthi1e4:name4:root6:pieces%seeeeeeeeeee", strings.Repeat("l", 10)))
	if _, err := Parse(data, Limits{MaxDepth: 2}); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseRejectsNULInNestedPath(t *testing.T) {
	data := bytes.ReplaceAll([]byte("d4:infod5:filesld6:lengthi1e4:pathl3:a\x00beee4:name4:rootee"), []byte("\\x00"), []byte{0})
	if _, err := Parse(data, Limits{}); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("err=%v", err)
	}
}
