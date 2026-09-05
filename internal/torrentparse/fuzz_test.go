package torrentparse

import "testing"

// FuzzParse 是 HARD-05 的 bencode corpus fuzz：任意输入不得 panic，且
// 成功解析的产物必须遵守 DefaultLimits 的所有上限。
func FuzzParse(f *testing.F) {
	f.Add([]byte("d4:infod4:name4:teste6:lengthi1eee"))
	f.Add([]byte("d4:infod6:pieces20:" + string(make([]byte, 20)) + "ee"))
	f.Add([]byte("d4:infod5:filesld4:name4:aaaeed4:name4:bbbeee"))
	f.Add([]byte("d4:infodi-1eee"))
	f.Add([]byte("d99999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999"))
	f.Add([]byte("de"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		torrent, err := Parse(data, DefaultLimits())
		if err != nil {
			return
		}
		limits := DefaultLimits()
		if torrent.TotalSize > limits.MaxTotalSize {
			t.Fatalf("total size exceeds limit: %d", torrent.TotalSize)
		}
		if len(torrent.Files) > limits.MaxFiles {
			t.Fatalf("file count exceeds limit: %d", len(torrent.Files))
		}
	})
}
