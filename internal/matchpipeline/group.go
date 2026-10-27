package matchpipeline

import (
	"regexp"
	"strings"
)

var (
	// Trailing subtitle blocks NexusPHP sites append: "Title-GRP [中文名]".
	trailingBracket = regexp.MustCompile(`\s*(\[[^\[\]]*\]|【[^【】]*】|\([^()]*\)|（[^（）]*）)\s*$`)
	// Scene/P2P suffix "...H.264-GROUP" or "...-GROUP@SITE"; the group itself
	// has no spaces, dots or dashes.
	suffixGroup = regexp.MustCompile(`-([A-Za-z0-9][A-Za-z0-9_]{1,31}(?:@[A-Za-z0-9_]{1,31})?)$`)
	// Fansub prefix "[Group] Title".
	prefixGroup = regexp.MustCompile(`^\[([^\[\]]{2,32})\]`)
)

// Source/format tokens that end a title after a dash without being a group,
// e.g. "1080p WEB-DL" or "BD-Rip".
var notGroups = map[string]bool{"DL": true, "RIP": true, "WEB": true, "HD": true, "HDR": true, "DV": true, "TS": true, "TC": true, "CAM": true}

// ReleaseGroup extracts the release group from a torrent title, or "" when the
// title has no recognisable group; an absent fact keeps "group" rules UNKNOWN
// rather than guessing.
func ReleaseGroup(title string) string {
	title = strings.TrimSpace(title)
	for {
		trimmed := trailingBracket.ReplaceAllString(title, "")
		if trimmed == title || trimmed == "" {
			break
		}
		title = trimmed
	}
	if match := suffixGroup.FindStringSubmatch(title); match != nil {
		group := match[1]
		name, _, _ := strings.Cut(group, "@")
		if !notGroups[strings.ToUpper(name)] {
			return group
		}
		return ""
	}
	if match := prefixGroup.FindStringSubmatch(title); match != nil {
		return strings.TrimSpace(match[1])
	}
	return ""
}
