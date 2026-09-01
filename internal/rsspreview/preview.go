package rsspreview

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
)

type Fetcher interface {
	Fetch(context.Context, string) ([]byte, error)
}
type Entry struct {
	ID        string
	Title     string
	Link      string
	Published string
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
	if len(document.Channel.Items) > 0 {
		return Preview{FeedURL: feedURL, Format: "rss", Entries: rssEntries(document.Channel.Items, limit)}, nil
	}
	var atom atomFeed
	if err := xml.Unmarshal(body, &atom); err == nil && len(atom.Entries) > 0 {
		return Preview{FeedURL: feedURL, Format: "atom", Entries: atomEntries(atom.Entries, limit)}, nil
	}
	return Preview{}, errors.New("feed contains no entries")
}

type xmlDocument struct {
	Channel rssChannel `xml:"channel"`
	Feed    atomFeed   `xml:"http://www.w3.org/2005/Atom feed"`
}
type rssChannel struct {
	Items []rssItem `xml:"item"`
}
type rssItem struct {
	GUID      string `xml:"guid"`
	Title     string `xml:"title"`
	Link      string `xml:"link"`
	Published string `xml:"pubDate"`
}
type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}
type atomEntry struct {
	ID      string     `xml:"id"`
	Title   string     `xml:"title"`
	Updated string     `xml:"updated"`
	Links   []atomLink `xml:"link"`
}
type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

func rssEntries(items []rssItem, limit int) []Entry {
	entries := make([]Entry, 0, min(limit, len(items)))
	for _, item := range items[:min(limit, len(items))] {
		entries = append(entries, Entry{ID: strings.TrimSpace(item.GUID), Title: strings.TrimSpace(item.Title), Link: strings.TrimSpace(item.Link), Published: strings.TrimSpace(item.Published)})
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
		entries = append(entries, Entry{ID: strings.TrimSpace(item.ID), Title: strings.TrimSpace(item.Title), Link: strings.TrimSpace(link), Published: strings.TrimSpace(item.Updated)})
	}
	return entries
}
func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
