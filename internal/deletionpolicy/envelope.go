package deletionpolicy

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Tags Vertex controls. Every safety decision is made on tags rather than
// trackers: qBittorrent's tracker field is empty exactly when a tracker
// fails (e.g. unregistered torrents), and a negated tracker condition would
// then pass. Tags are local qB metadata and are always present.
const (
	// ManagedTag marks torrents Vertex added. Nothing else is ever deleted.
	ManagedTag = "vertex-managed"
	// KeepTag protects one torrent permanently (or until Vertex removes it
	// when a timed protection expires).
	KeepTag = "vertex-keep"
	// SiteTagPrefix + site ID attributes a torrent to its site. A torrent
	// without any site tag is never deleted: its H&R floor is unknown.
	SiteTagPrefix = "vertex-site-"
)

// SiteTag returns the attribution tag for a site.
func SiteTag(site string) string { return SiteTagPrefix + site }

// ValidSite reports whether a site ID can be carried in a qB tag.
func ValidSite(site string) bool { return sitePattern.MatchString(site) }

// DeliveryTags returns tags with Vertex's ownership tags added for a torrent
// delivered from site. An invalid site gets no site tag, which keeps the
// torrent out of every deletion policy rather than mis-attributing it.
func DeliveryTags(tags []string, site string) []string {
	result := append(slices.Clone(tags), ManagedTag)
	if ValidSite(site) {
		result = append(result, SiteTag(site))
	}
	return result
}

// HitAndRun is a site's seeding requirement. A torrent of that site may only
// be deleted once it seeded MinSeedingTime or reached MinRatio (when set).
type HitAndRun struct {
	MinSeedingTime time.Duration
	MinRatio       float64
}

// Safety is the operator-wide protection every compiled automation carries.
type Safety struct {
	ProtectedCategories []string
	ProtectedSites      []string
	HitAndRun           map[string]HitAndRun
}

func (safety Safety) validate() error {
	for _, category := range safety.ProtectedCategories {
		if strings.TrimSpace(category) == "" || strings.Contains(category, ",") {
			return invalid("protected category %q is invalid", category)
		}
	}
	for _, site := range safety.ProtectedSites {
		if !sitePattern.MatchString(site) {
			return invalid("protected site %q is invalid", site)
		}
	}
	for site, rule := range safety.HitAndRun {
		if !sitePattern.MatchString(site) {
			return invalid("H&R site %q is invalid", site)
		}
		if rule.MinSeedingTime < 0 || rule.MinSeedingTime%time.Second != 0 || !finite(rule.MinRatio) || rule.MinRatio < 0 {
			return invalid("H&R rule for %s has invalid values", site)
		}
		if rule.MinSeedingTime == 0 && rule.MinRatio == 0 {
			return invalid("H&R rule for %s needs a seeding time or ratio", site)
		}
	}
	return nil
}

// envelope returns the conditions ANDed in front of every policy condition.
// complete selects the finished-torrent envelope; stalled cleanup instead
// requires unfinished torrents and excludes H&R sites entirely.
func (safety Safety) envelope(complete bool) []*Condition {
	conditions := []*Condition{
		leaf("TAGS", "EQUAL", ManagedTag),
		leaf("TAGS", "STARTS_WITH", SiteTagPrefix),
		leaf("TAGS", "NOT_EQUAL", KeepTag),
	}
	for _, category := range sortedUnique(safety.ProtectedCategories) {
		conditions = append(conditions, leaf("CATEGORY", "NOT_EQUAL", category))
	}
	for _, site := range sortedUnique(safety.ProtectedSites) {
		conditions = append(conditions, leaf("TAGS", "NOT_EQUAL", SiteTag(site)))
	}
	sites := make([]string, 0, len(safety.HitAndRun))
	for site := range safety.HitAndRun {
		sites = append(sites, site)
	}
	slices.Sort(sites)
	if !complete {
		conditions = append(conditions, leaf("AMOUNT_LEFT", "GREATER_THAN", "0"))
		for _, site := range sites {
			conditions = append(conditions, leaf("TAGS", "NOT_EQUAL", SiteTag(site)))
		}
		return conditions
	}
	conditions = append(conditions, leaf("AMOUNT_LEFT", "EQUAL", "0"))
	for _, site := range sites {
		rule := safety.HitAndRun[site]
		floor := []*Condition{leaf("TAGS", "NOT_EQUAL", SiteTag(site))}
		if rule.MinSeedingTime > 0 {
			floor = append(floor, leaf("SEEDING_TIME", "GREATER_THAN_OR_EQUAL", seconds(rule.MinSeedingTime)))
		}
		if rule.MinRatio > 0 {
			floor = append(floor, leaf("RATIO", "GREATER_THAN_OR_EQUAL", decimal(rule.MinRatio)))
		}
		conditions = append(conditions, group("OR", floor...))
	}
	return conditions
}

func sortedUnique(values []string) []string {
	result := slices.Clone(values)
	slices.Sort(result)
	return slices.Compact(result)
}

func seconds(duration time.Duration) string {
	return fmt.Sprintf("%d", int64(duration/time.Second))
}
