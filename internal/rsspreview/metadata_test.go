package rsspreview

import "testing"

func TestParseReadsCategoryAndPublisher(t *testing.T) {
	rss := `<?xml version="1.0"?><rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/"><channel>
<item><guid>1</guid><title>A</title><category domain="https://pt.example/?cat=401"> Movies </category><category>HD</category><author>uploader@pt.example (Alice)</author><dc:creator>Alice</dc:creator></item>
<item><guid>2</guid><title>B</title><category></category><category>TV  Series</category><author>Bob</author></item>
<item><guid>3</guid><title>C</title></item>
</channel></rss>`
	preview, err := Parse("https://pt.example/rss", []byte(rss), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"Movies", "Alice"}, {"TV Series", "Bob"}, {"", ""}}
	for index, entry := range preview.Entries {
		if entry.Category != want[index][0] || entry.Publisher != want[index][1] {
			t.Fatalf("rss entry %d = %q/%q, want %q/%q", index, entry.Category, entry.Publisher, want[index][0], want[index][1])
		}
	}
	atom := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
<entry><id>1</id><title>A</title><category term="401" label="Movies"/><author><name>Alice</name></author></entry>
<entry><id>2</id><title>B</title><category term="tv"/></entry>
</feed>`
	preview, err = Parse("https://pt.example/atom", []byte(atom), 10)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Entries[0].Category != "Movies" || preview.Entries[0].Publisher != "Alice" || preview.Entries[1].Category != "tv" || preview.Entries[1].Publisher != "" {
		t.Fatalf("atom entries = %+v", preview.Entries)
	}
}
