package rsspreview

import (
	"strings"
	"testing"
)

// FuzzParse 是 HARD-05 的 RSS 解析 corpus fuzz：任意输入不得 panic，且
// 条目数量上限必须被强制执行。
func FuzzParse(f *testing.F) {
	f.Add("https://pt.example/feed", []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>S</title><item><guid>g</guid><title>T</title><link>https://pt.example/t</link></item></channel></rss>`))
	f.Add("https://pt.example/feed", []byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry><title>A</title></entry></feed>`))
	f.Add("https://pt.example/feed", []byte("not xml at all"))
	f.Add("", []byte(`<rss><channel><item><title>`+strings.Repeat("A", 4096)+`</title></item></channel></rss>`))
	f.Add("https://pt.example/feed", []byte(`<rss><channel>`+strings.Repeat(`<item><guid>g</guid></item>`, 64)+`</channel></rss>`))
	f.Fuzz(func(t *testing.T, feedURL string, body []byte) {
		preview, err := Parse(feedURL, body, 20)
		if err != nil {
			return
		}
		if len(preview.Entries) > 20 {
			t.Fatalf("entry limit exceeded: %d", len(preview.Entries))
		}
	})
}
