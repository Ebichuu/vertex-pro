package deletionpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- A model of qui's evaluator (internal/services/automations/evaluator.go
// at bfe94b5e) for the fields and operators the compiler emits. It keeps
// qui's quirks that matter for safety: string and tag comparisons are
// case-insensitive, tags are a set, NOT_EQUAL is true when nothing equals,
// and an EMPTY numeric value compares as 0.

type torrent struct {
	Tags                                         []string
	Category                                     string
	AmountLeft, SeedingTime, AddedAge, LastAge   int64
	Uploaded, UpSpeedAvg, UpSpeed, Size          int64
	NumLeechs, NumIncomplete, NumSeeds, NumCompl int64
	Ratio, UploadedOverSize                      float64
	Unregistered                                 bool
}

func evaluate(t *testing.T, condition *Condition, subject torrent, freeSpace int64) bool {
	if len(condition.Conditions) > 0 {
		result := condition.Operator == "AND"
		for _, child := range condition.Conditions {
			value := evaluate(t, child, subject, freeSpace)
			if condition.Operator == "AND" && !value {
				result = false
				break
			}
			if condition.Operator == "OR" && value {
				result = true
				break
			}
		}
		return result != condition.Negate
	}
	result := evaluateLeaf(t, condition, subject, freeSpace)
	return result != condition.Negate
}

func evaluateLeaf(t *testing.T, condition *Condition, subject torrent, freeSpace int64) bool {
	integer := func(value int64) bool {
		wanted, err := strconv.ParseInt(strings.TrimSpace(condition.Value), 10, 64)
		if err != nil && condition.Value != "" {
			return false
		}
		return compare(condition.Operator, float64(value), float64(wanted))
	}
	floating := func(value float64) bool {
		wanted, err := strconv.ParseFloat(strings.TrimSpace(condition.Value), 64)
		if err != nil && condition.Value != "" {
			return false
		}
		return compare(condition.Operator, value, wanted)
	}
	switch condition.Field {
	case "TAGS":
		wanted := strings.ToLower(strings.TrimSpace(condition.Value))
		match := func(test func(tag string) bool) bool {
			for _, tag := range subject.Tags {
				if test(strings.ToLower(tag)) {
					return true
				}
			}
			return false
		}
		switch condition.Operator {
		case "EQUAL":
			return match(func(tag string) bool { return tag == wanted })
		case "NOT_EQUAL":
			return !match(func(tag string) bool { return tag == wanted })
		case "STARTS_WITH":
			return match(func(tag string) bool { return strings.HasPrefix(tag, wanted) })
		}
	case "CATEGORY":
		switch condition.Operator {
		case "EQUAL":
			return strings.EqualFold(subject.Category, condition.Value)
		case "NOT_EQUAL":
			return !strings.EqualFold(subject.Category, condition.Value)
		}
	case "AMOUNT_LEFT":
		return integer(subject.AmountLeft)
	case "SEEDING_TIME":
		return integer(subject.SeedingTime)
	case "ADDED_ON_AGE":
		return integer(subject.AddedAge)
	case "LAST_ACTIVITY_AGE":
		return integer(subject.LastAge)
	case "UPLOADED":
		return integer(subject.Uploaded)
	case "UP_SPEED_AVG":
		return integer(subject.UpSpeedAvg)
	case "UP_SPEED":
		return integer(subject.UpSpeed)
	case "SIZE":
		return integer(subject.Size)
	case "NUM_LEECHS":
		return integer(subject.NumLeechs)
	case "NUM_INCOMPLETE":
		return integer(subject.NumIncomplete)
	case "NUM_SEEDS":
		return integer(subject.NumSeeds)
	case "NUM_COMPLETE":
		return integer(subject.NumCompl)
	case "RATIO":
		return floating(subject.Ratio)
	case "UPLOADED_OVER_SIZE":
		return floating(subject.UploadedOverSize)
	case "FREE_SPACE":
		return integer(freeSpace)
	case "IS_UNREGISTERED":
		wanted := strings.EqualFold(condition.Value, "true") || condition.Value == "1"
		return subject.Unregistered == wanted
	}
	t.Fatalf("model evaluator does not know %s %s", condition.Field, condition.Operator)
	return false
}

func compare(operator string, value, wanted float64) bool {
	switch operator {
	case "EQUAL":
		return value == wanted
	case "GREATER_THAN":
		return value > wanted
	case "GREATER_THAN_OR_EQUAL":
		return value >= wanted
	case "LESS_THAN":
		return value < wanted
	case "LESS_THAN_OR_EQUAL":
		return value <= wanted
	}
	return false
}

// ---- Random generators.

var sites = []string{"siteA", "siteB", "siteC", "siteD"}

func randomThreshold(r *rand.Rand) Threshold {
	all := []Metric{SeedingTime, AddedAge, LastActivityAge, Ratio, UploadedOverSize, Uploaded, UpSpeedAvg, UpSpeed, NumLeechs, NumIncomplete, NumSeeds, NumComplete, Size, Unregistered}
	metric := all[r.Intn(len(all))]
	if metric == Unregistered {
		return Threshold{Metric: metric}
	}
	ops := []Comparison{GreaterThan, GreaterOrEqual, LessThan, LessOrEqual}
	threshold := Threshold{Metric: metric, Op: ops[r.Intn(len(ops))]}
	switch spec := metrics[metric]; {
	case spec.duration:
		threshold.Duration = time.Duration(r.Intn(200)) * 5 * time.Minute
	case spec.float:
		threshold.Value = float64(r.Intn(50)) / 10
	default:
		threshold.Value = float64(r.Intn(1 << 20))
	}
	return threshold
}

func randomPolicy(r *rand.Rand) Policy {
	modes := []DeleteMode{KeepFiles, WithFiles, WithFilesPreserveCrossSeeds}
	policy := Policy{ID: fmt.Sprintf("p%d", r.Intn(1000)), Name: "random", Revision: 1 + r.Intn(5), Live: r.Intn(2) == 0, DeleteMode: modes[r.Intn(len(modes))]}
	for range r.Intn(4) {
		trigger := Trigger{}
		for range 1 + r.Intn(3) {
			trigger.All = append(trigger.All, randomThreshold(r))
		}
		policy.Triggers = append(policy.Triggers, trigger)
	}
	if r.Intn(2) == 0 {
		watermark := &Watermark{MinFreeBytes: int64(1+r.Intn(500)) << 30, Order: []Weight{{Metric: Size, Multiplier: 1e-9}}}
		for range r.Intn(3) {
			watermark.Eligible = append(watermark.Eligible, randomThreshold(r))
		}
		if r.Intn(2) == 0 {
			watermark.SiteScores = map[string]float64{sites[r.Intn(len(sites))]: float64(r.Intn(20) - 10)}
		}
		policy.Watermark = watermark
	}
	if r.Intn(3) == 0 {
		policy.Stalled = &StalledCleanup{MinAddedAge: time.Duration(30+r.Intn(300)) * time.Minute, MinLastActivityAge: time.Duration(30+r.Intn(300)) * time.Minute}
	}
	return policy
}

func randomSafety(r *rand.Rand) Safety {
	safety := Safety{HitAndRun: map[string]HitAndRun{}}
	if r.Intn(2) == 0 {
		safety.ProtectedCategories = []string{"keep"}
	}
	if r.Intn(2) == 0 {
		safety.ProtectedSites = []string{sites[r.Intn(len(sites))]}
	}
	for _, site := range sites {
		if r.Intn(3) == 0 {
			safety.HitAndRun[site] = HitAndRun{MinSeedingTime: time.Duration(r.Intn(4)) * 24 * time.Hour, MinRatio: float64(r.Intn(3))}
			if safety.HitAndRun[site] == (HitAndRun{}) {
				safety.HitAndRun[site] = HitAndRun{MinRatio: 1}
			}
		}
	}
	return safety
}

func randomTorrent(r *rand.Rand) torrent {
	candidates := []string{ManagedTag, KeepTag, "Vertex-Managed", "VERTEX-KEEP", "other", "vertex-managed-ish"}
	for _, site := range sites {
		candidates = append(candidates, SiteTag(site))
	}
	var tags []string
	for _, tag := range candidates {
		if r.Intn(3) == 0 {
			tags = append(tags, tag)
		}
	}
	categories := []string{"", "brush", "keep", "KEEP"}
	subject := torrent{
		Tags: tags, Category: categories[r.Intn(len(categories))],
		SeedingTime: int64(r.Intn(10 * 24 * 3600)), AddedAge: int64(r.Intn(10 * 24 * 3600)), LastAge: int64(r.Intn(48 * 3600)),
		Uploaded: int64(r.Intn(1 << 30)), UpSpeedAvg: int64(r.Intn(1 << 20)), UpSpeed: int64(r.Intn(1 << 20)), Size: int64(r.Intn(1 << 30)),
		NumLeechs: int64(r.Intn(20)), NumIncomplete: int64(r.Intn(20)), NumSeeds: int64(r.Intn(20)), NumCompl: int64(r.Intn(100)),
		Ratio: float64(r.Intn(60)) / 10, UploadedOverSize: float64(r.Intn(60)) / 10, Unregistered: r.Intn(4) == 0,
	}
	if r.Intn(2) == 0 {
		subject.AmountLeft = int64(1 + r.Intn(1<<20))
	}
	return subject
}

// ---- The envelope stated independently of the compiler, in plain Go.

func hasTag(subject torrent, tag string) bool {
	for _, candidate := range subject.Tags {
		if strings.EqualFold(candidate, tag) {
			return true
		}
	}
	return false
}

func envelopeAllows(subject torrent, safety Safety, role Role) error {
	if !hasTag(subject, ManagedTag) {
		return errors.New("torrent is not Vertex-managed")
	}
	if hasTag(subject, KeepTag) {
		return errors.New("torrent carries the keep tag")
	}
	site := ""
	for _, tag := range subject.Tags {
		if strings.HasPrefix(strings.ToLower(tag), SiteTagPrefix) {
			site = tag[len(SiteTagPrefix):]
		}
	}
	if site == "" {
		return errors.New("torrent has no site attribution")
	}
	for _, category := range safety.ProtectedCategories {
		if strings.EqualFold(subject.Category, category) {
			return errors.New("category is protected")
		}
	}
	for _, protected := range safety.ProtectedSites {
		if hasTag(subject, SiteTag(protected)) {
			return errors.New("site is protected")
		}
	}
	if role == RoleStalled {
		if subject.AmountLeft == 0 {
			return errors.New("stalled cleanup reached a complete torrent")
		}
		for hnr := range safety.HitAndRun {
			if hasTag(subject, SiteTag(hnr)) {
				return errors.New("stalled cleanup reached an H&R site")
			}
		}
		return nil
	}
	if subject.AmountLeft != 0 {
		return errors.New("incomplete torrent reached a completed-torrent role")
	}
	for hnr, rule := range safety.HitAndRun {
		if !hasTag(subject, SiteTag(hnr)) {
			continue
		}
		seeded := rule.MinSeedingTime > 0 && subject.SeedingTime >= int64(rule.MinSeedingTime/time.Second)
		shared := rule.MinRatio > 0 && subject.Ratio >= rule.MinRatio
		if !seeded && !shared {
			return fmt.Errorf("H&R floor of %s not met", hnr)
		}
	}
	return nil
}

// ---- Properties.

func walk(condition *Condition, visit func(*Condition)) {
	visit(condition)
	for _, child := range condition.Conditions {
		walk(child, visit)
	}
}

var numericFields = map[string]bool{"AMOUNT_LEFT": true, "SEEDING_TIME": true, "ADDED_ON_AGE": true, "LAST_ACTIVITY_AGE": true, "UPLOADED": true, "UP_SPEED_AVG": true, "UP_SPEED": true, "SIZE": true, "NUM_LEECHS": true, "NUM_INCOMPLETE": true, "NUM_SEEDS": true, "NUM_COMPLETE": true, "FREE_SPACE": true, "RATIO": true, "UPLOADED_OVER_SIZE": true}

func TestCompiledAutomationsAlwaysCarryTheEnvelope(t *testing.T) {
	r := rand.New(rand.NewSource(20261027))
	compiledPolicies, rejected := 0, 0
	for iteration := 0; iteration < 3000; iteration++ {
		policy, safety := randomPolicy(r), randomSafety(r)
		compiled, err := Compile(policy, safety)
		if err != nil {
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("iteration %d: error is not ErrInvalid: %v", iteration, err)
			}
			rejected++
			continue
		}
		compiledPolicies++
		for _, item := range compiled {
			root := item.Delete.Condition
			want := safety.envelope(item.Role != RoleStalled)
			if root.Operator != "AND" || root.Negate || len(root.Conditions) < len(want) || !reflect.DeepEqual(root.Conditions[:len(want)], want) {
				t.Fatalf("iteration %d %s: envelope missing or altered", iteration, item.Role)
			}
			walk(root, func(condition *Condition) {
				if len(condition.Conditions) > 0 {
					if condition.Field != "" || (condition.Operator != "AND" && condition.Operator != "OR") {
						t.Fatalf("iteration %d: malformed group %+v", iteration, condition)
					}
					return
				}
				if condition.Operator == "AND" || condition.Operator == "OR" {
					t.Fatalf("iteration %d: empty group", iteration)
				}
				if condition.Field == "" || condition.Value == "" || condition.Operator == "MATCHES" {
					t.Fatalf("iteration %d: unsafe leaf %+v", iteration, condition)
				}
				if numericFields[condition.Field] {
					if _, err := strconv.ParseFloat(condition.Value, 64); err != nil {
						t.Fatalf("iteration %d: non-numeric value %q for %s", iteration, condition.Value, condition.Field)
					}
				}
			})
			if item.Delete.Mode == "deleteWithFilesIncludeCrossSeeds" || !item.Delete.Enabled {
				t.Fatalf("iteration %d: delete action %+v", iteration, item.Delete)
			}
			if item.Automation.DryRun == policy.Live || item.Automation.Enabled != policy.Live {
				t.Fatalf("iteration %d: live=%v but enabled=%v dryRun=%v", iteration, policy.Live, item.Automation.Enabled, item.Automation.DryRun)
			}
			if item.Automation.Name != AutomationName(policy.ID, item.Role) {
				t.Fatalf("iteration %d: name %q", iteration, item.Automation.Name)
			}
			// The map sent to qui is exactly the typed action.
			encoded, _ := json.Marshal(item.Automation.Conditions["delete"])
			var decoded DeleteAction
			if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, item.Delete) {
				t.Fatalf("iteration %d: automation JSON differs from typed action", iteration)
			}
			// Semantics: whatever this automation would delete satisfies the
			// envelope as stated in plain Go above.
			for sample := 0; sample < 40; sample++ {
				subject := randomTorrent(r)
				freeSpace := int64(r.Intn(600)) << 30
				if evaluate(t, root, subject, freeSpace) {
					if err := envelopeAllows(subject, safety, item.Role); err != nil {
						t.Fatalf("iteration %d %s: compiled condition matched a protected torrent: %v\n%+v", iteration, item.Role, err, subject)
					}
				}
			}
		}
	}
	if compiledPolicies < 500 || rejected < 100 {
		t.Fatalf("generator coverage too narrow: compiled=%d rejected=%d", compiledPolicies, rejected)
	}
}

func TestValidationRejectsUnsafePolicies(t *testing.T) {
	base := func() Policy {
		return Policy{ID: "p", Name: "p", Revision: 1, DeleteMode: WithFiles, Triggers: []Trigger{{All: []Threshold{{Metric: SeedingTime, Op: GreaterOrEqual, Duration: time.Hour}}}}}
	}
	cases := map[string]func(*Policy){
		"no anchor (deletes fresh torrents)": func(p *Policy) { p.Triggers[0].All = []Threshold{{Metric: NumLeechs, Op: LessThan, Value: 1}} },
		"anchor below hold floor":            func(p *Policy) { p.Triggers[0].All[0].Duration = 5 * time.Minute },
		"upper-bound anchor":                 func(p *Policy) { p.Triggers[0].All[0].Op = LessThan },
		"zero ratio anchor":                  func(p *Policy) { p.Triggers[0].All = []Threshold{{Metric: Ratio, Op: GreaterThan, Value: 0}} },
		"cross-seed deleting mode":           func(p *Policy) { p.DeleteMode = "with_files_include_cross_seeds" },
		"empty policy":                       func(p *Policy) { p.Triggers = nil },
		"empty trigger":                      func(p *Policy) { p.Triggers = []Trigger{{}} },
		"negative value": func(p *Policy) {
			p.Triggers[0].All = append(p.Triggers[0].All, Threshold{Metric: Size, Op: GreaterThan, Value: -1})
		},
		"fractional integer metric": func(p *Policy) {
			p.Triggers[0].All = append(p.Triggers[0].All, Threshold{Metric: Size, Op: GreaterThan, Value: 1.5})
		},
		"NaN value": func(p *Policy) {
			p.Triggers[0].All = append(p.Triggers[0].All, Threshold{Metric: Ratio, Op: GreaterThan, Value: nan()})
		},
		"duration metric with value": func(p *Policy) { p.Triggers[0].All[0].Value = 10 },
		"sub-second duration":        func(p *Policy) { p.Triggers[0].All[0].Duration = time.Hour + time.Millisecond },
		"unregistered with operator": func(p *Policy) { p.Triggers[0].All = []Threshold{{Metric: Unregistered, Op: GreaterThan}} },
		"unknown metric": func(p *Policy) {
			p.Triggers[0].All = append(p.Triggers[0].All, Threshold{Metric: "free", Op: GreaterThan})
		},
		"watermark without target": func(p *Policy) { p.Watermark = &Watermark{Order: []Weight{{Metric: Size, Multiplier: 1}}} },
		"watermark without order":  func(p *Policy) { p.Watermark = &Watermark{MinFreeBytes: 1} },
		"watermark relative path": func(p *Policy) {
			p.Watermark = &Watermark{MinFreeBytes: 1, FreeSpacePath: "data", Order: []Weight{{Metric: Size, Multiplier: 1}}}
		},
		"unsortable weight": func(p *Policy) {
			p.Watermark = &Watermark{MinFreeBytes: 1, Order: []Weight{{Metric: Unregistered, Multiplier: 1}}}
		},
		"stalled too eager":  func(p *Policy) { p.Stalled = &StalledCleanup{MinAddedAge: time.Minute, MinLastActivityAge: time.Hour} },
		"interval too short": func(p *Policy) { p.Interval = 10 * time.Second },
		"bad ID":             func(p *Policy) { p.ID = "../x" },
		"zero revision":      func(p *Policy) { p.Revision = 0 },
	}
	for name, mutate := range cases {
		policy := base()
		mutate(&policy)
		if _, err := Compile(policy, Safety{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	if _, err := Compile(base(), Safety{}); err != nil {
		t.Fatalf("base policy: %v", err)
	}
	for name, safety := range map[string]Safety{
		"empty H&R":         {HitAndRun: map[string]HitAndRun{"siteA": {}}},
		"negative ratio":    {HitAndRun: map[string]HitAndRun{"siteA": {MinRatio: -1}}},
		"bad site":          {ProtectedSites: []string{"a b"}},
		"comma in category": {ProtectedCategories: []string{"a,b"}},
		"blank category":    {ProtectedCategories: []string{" "}},
	} {
		if _, err := Compile(base(), safety); !errors.Is(err, ErrInvalid) {
			t.Errorf("safety %s: error = %v", name, err)
		}
	}
}

func nan() float64 { zero := 0.0; return zero / zero }
