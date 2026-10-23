package rsspreview

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type Fetcher interface {
	Fetch(context.Context, string) ([]byte, error)
}
type Entry struct {
	ID          string
	Title       string
	Link        string
	Published   string
	DownloadURL string
	SizeBytes   *int64
}
type Preview struct {
	FeedURL string
	Format  string
	Entries []Entry
}

func Test(ctx context.Context, fetcher Fetcher, feedURL string, limit int) (Preview, error) {
	if fetcher == nil {
		return Preview{}, errors.New("fetcher is required")
	}
	feedURL = strings.TrimSpace(feedURL)
	if feedURL == "" {
		return Preview{}, errors.New("feed URL is required")
	}
	if limit <= 0 || limit > 100 {
		return Preview{}, errors.New("limit must be between 1 and 100")
	}
	body, err := fetcher.Fetch(ctx, feedURL)
	if err != nil {
		return Preview{}, fmt.Errorf("fetch feed preview: %w", err)
	}
	return Parse(feedURL, body, limit)
}

func Parse(feedURL string, body []byte, limit int) (Preview, error) {
	if len(body) == 0 {
		return Preview{}, errors.New("feed body is empty")
	}
	if limit <= 0 || limit > 100 {
		return Preview{}, errors.New("limit must be between 1 and 100")
	}
	var document xmlDocument
	if err := xml.Unmarshal(body, &document); err != nil {
		return Preview{}, fmt.Errorf("parse feed XML: %w", err)
	}
	if document.XMLName.Local == "rss" {
		return Preview{FeedURL: feedURL, Format: "rss", Entries: resolveEntries(feedURL, rssEntries(document.Channel.Items, limit))}, nil
	}
	var atom atomFeed
	if err := xml.Unmarshal(body, &atom); err == nil && atom.XMLName.Local == "feed" {
		return Preview{FeedURL: feedURL, Format: "atom", Entries: resolveEntries(feedURL, atomEntries(atom.Entries, limit))}, nil
	}
	return Preview{}, errors.New("feed contains no entries")
}

type xmlDocument struct {
	XMLName xml.Name
	Channel rssChannel `xml:"channel"`
	Feed    atomFeed   `xml:"http://www.w3.org/2005/Atom feed"`
}
type rssChannel struct {
	Items []rssItem `xml:"item"`
}
type rssItem struct {
	GUID       string      `xml:"guid"`
	Title      string      `xml:"title"`
	Link       string      `xml:"link"`
	Published  string      `xml:"pubDate"`
	Enclosures []enclosure `xml:"enclosure"`
}
type enclosure struct {
	URL    string `xml:"url,attr"`
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
}
type atomFeed struct {
	XMLName xml.Name
	Entries []atomEntry `xml:"entry"`
}
type atomEntry struct {
	ID      string     `xml:"id"`
	Title   string     `xml:"title"`
	Updated string     `xml:"updated"`
	Links   []atomLink `xml:"link"`
}
type atomLink struct {
	Href   string `xml:"href,attr"`
	Rel    string `xml:"rel,attr"`
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
}

func rssEntries(items []rssItem, limit int) []Entry {
	entries := make([]Entry, 0, min(limit, len(items)))
	for _, item := range items[:min(limit, len(items))] {
		entry := Entry{ID: strings.TrimSpace(item.GUID), Title: strings.TrimSpace(item.Title), Link: strings.TrimSpace(item.Link), Published: strings.TrimSpace(item.Published)}
		for _, candidate := range item.Enclosures {
			if candidate.Type == "application/x-bittorrent" || candidate.Type == "" {
				entry.DownloadURL = strings.TrimSpace(candidate.URL)
				entry.SizeBytes = enclosureSize(candidate.Length)
				break
			}
		}
		entries = append(entries, entry)
	}
	return entries
}
func atomEntries(items []atomEntry, limit int) []Entry {
	entries := make([]Entry, 0, min(limit, len(items)))
	for _, item := range items[:min(limit, len(items))] {
		link := ""
		for _, candidate := range item.Links {
			if candidate.Rel == "" || candidate.Rel == "alternate" {
				link = candidate.Href
				break
			}
		}
		entry := Entry{ID: strings.TrimSpace(item.ID), Title: strings.TrimSpace(item.Title), Link: strings.TrimSpace(link), Published: strings.TrimSpace(item.Updated)}
		for _, candidate := range item.Links {
			if candidate.Rel == "enclosure" && (candidate.Type == "application/x-bittorrent" || candidate.Type == "") {
				entry.DownloadURL = strings.TrimSpace(candidate.Href)
				entry.SizeBytes = enclosureSize(candidate.Length)
				break
			}
		}
		entries = append(entries, entry)
	}
	return entries
}
func enclosureSize(raw string) *int64 {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return nil
	}
	return &value
}
func resolveEntries(feedURL string, entries []Entry) []Entry {
	base, err := url.Parse(feedURL)
	if err != nil {
		return entries
	}
	for index := range entries {
		for _, field := range []*string{&entries[index].Link, &entries[index].DownloadURL} {
			if *field == "" {
				continue
			}
			if reference, err := url.Parse(*field); err == nil {
				*field = base.ResolveReference(reference).String()
			}
		}
	}
	return entries
}
func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
