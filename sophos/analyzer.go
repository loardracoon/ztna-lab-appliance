package sophos

import (
	"fmt"
	"sort"
	"strings"
)

// Severity ranks a finding. It drives ordering in the report and the
// -min-severity filter.
type Severity int

// Severity levels, most serious first.
const (
	SeverityInfo Severity = iota
	SeverityLow
	SeverityMedium
	SeverityHigh
)

func (s Severity) String() string {
	switch s {
	case SeverityHigh:
		return "high"
	case SeverityMedium:
		return "medium"
	case SeverityLow:
		return "low"
	default:
		return "info"
	}
}

// ParseSeverity maps a flag value onto a level.
func ParseSeverity(s string) (Severity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info", "":
		return SeverityInfo, nil
	case "low":
		return SeverityLow, nil
	case "medium", "med":
		return SeverityMedium, nil
	case "high":
		return SeverityHigh, nil
	}
	return 0, fmt.Errorf("sophos: unknown severity %q (info|low|medium|high)", s)
}

// Kind identifies what the analyzer found.
type Kind string

// The checks this tool runs.
const (
	KindShadowed     Kind = "shadowed"      // dead rule, and its intent is defeated
	KindRedundant    Kind = "redundant"     // dead rule, removing it changes nothing
	KindDuplicate    Kind = "duplicate"     // a copy of an earlier rule
	KindMergeable    Kind = "mergeable"     // two adjacent rules that could be one
	KindPermissive   Kind = "permissive"    // accept any source to any destination on any service
	KindBroadService Kind = "broad-service" // accept on any service
	KindNoLogging    Kind = "no-logging"    // matches are invisible to the log
	KindDisabled     Kind = "disabled"      // switched off, still in the rule base
	KindNoInspection Kind = "no-inspection" // accept with no security profile at all
)

// RuleRef points at a rule in the analyzed base.
type RuleRef struct {
	Position int    `json:"position"` // 1-based evaluation order
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
}

func refOf(r *Rule, idx int) RuleRef {
	return RuleRef{Position: idx + 1, ID: r.ID, Name: r.Name}
}

func (r RuleRef) String() string {
	if r.Name == "" {
		return fmt.Sprintf("#%d", r.Position)
	}
	return fmt.Sprintf("#%d %s", r.Position, r.Name)
}

// Finding is one thing worth doing something about.
type Finding struct {
	ID       string    `json:"id"`
	Kind     Kind      `json:"kind"`
	Severity Severity  `json:"-"`
	Level    string    `json:"severity"`
	Subject  RuleRef   `json:"subject"`
	Related  []RuleRef `json:"related,omitempty"`
	Title    string    `json:"title"`
	Detail   string    `json:"detail"`
	Actions  []Action  `json:"actions,omitempty"`
}

// Report is the result of one analysis run.
type Report struct {
	Source   string    `json:"source"`
	Rules    int       `json:"rules"`
	Enabled  int       `json:"enabledRules"`
	Findings []Finding `json:"findings"`

	// rules is the normalized rule base the findings refer to. The plan
	// builder needs it to construct merge patches.
	rules []Rule
}

// RuleBase returns the normalized rule base the findings refer to.
func (r *Report) RuleBase() []Rule { return r.rules }

// Count returns how many findings carry the given kind.
func (r *Report) Count(k Kind) int {
	n := 0
	for i := range r.Findings {
		if r.Findings[i].Kind == k {
			n++
		}
	}
	return n
}

// Options tunes which checks run.
type Options struct {
	// MinSeverity drops findings below this level.
	MinSeverity Severity
	// Skip lists check kinds to leave out entirely.
	Skip []Kind
	// Source labels where the rules came from, for the report header.
	Source string
}

func (o Options) skipped(k Kind) bool {
	for _, s := range o.Skip {
		if s == k {
			return true
		}
	}
	return false
}

// Analyze runs every check over an ordered rule base.
//
// rules must be in evaluation order — the order GET /firewall/rules/ipv4
// returns them in. The analyzer reasons entirely about that order, so a
// re-sorted slice produces nonsense.
func Analyze(rules []Rule, opt Options) *Report {
	norm := normalizeRules(rules)
	rep := &Report{Source: opt.Source, Rules: len(norm), rules: norm}

	profiles := make([]profile, len(norm))
	for i := range norm {
		profiles[i] = profileOf(&norm[i])
		if norm[i].IsEnabled() {
			rep.Enabled++
		}
	}

	var out []Finding
	out = append(out, coverageFindings(norm, profiles, opt)...)
	out = append(out, mergeFindings(norm, profiles, opt)...)
	out = append(out, hygieneFindings(norm, profiles, opt)...)

	// A rule already slated for removal does not also need its logging
	// fixed or its permissiveness pointed out.
	out = suppressNoise(out)

	filtered := out[:0]
	for _, f := range out {
		if f.Severity >= opt.MinSeverity {
			f.Level = f.Severity.String()
			filtered = append(filtered, f)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Severity != filtered[j].Severity {
			return filtered[i].Severity > filtered[j].Severity
		}
		return filtered[i].Subject.Position < filtered[j].Subject.Position
	})
	rep.Findings = filtered
	return rep
}

// coverageFindings reports rules an earlier rule already swallows.
func coverageFindings(rules []Rule, profiles []profile, opt Options) []Finding {
	var out []Finding
	for j := range rules {
		later := &rules[j]
		if !later.IsEnabled() || later.RuleType != RuleTypeFirewall {
			continue
		}
		for i := 0; i < j; i++ {
			earlier := &rules[i]
			if !coverageAllowed(earlier, later) {
				continue
			}
			if !profiles[i].covers(profiles[j]) {
				continue
			}

			// The first covering rule is the one that actually eats the
			// traffic, so report that one and stop.
			f := buildCoverageFinding(earlier, i, later, j, profiles)
			if f != nil && !opt.skipped(f.Kind) {
				out = append(out, *f)
			}
			break
		}
	}
	return out
}

func buildCoverageFinding(earlier *Rule, i int, later *Rule, j int, profiles []profile) *Finding {
	subject := refOf(later, j)
	anchor := refOf(earlier, i)
	equal := profiles[i].equal(profiles[j])
	same := sameOutcome(earlier, later)

	f := &Finding{
		Subject: subject,
		Related: []RuleRef{anchor},
	}

	switch {
	case equal && same:
		f.Kind = KindDuplicate
		f.Severity = SeverityMedium
		f.Title = fmt.Sprintf("%s duplicates %s", subject, anchor)
		f.Detail = fmt.Sprintf(
			"Rule %s matches exactly the same traffic as %s above it and treats it the same way, so it never sees a packet. Deleting it changes nothing.",
			subject, anchor)
		f.Actions = []Action{deleteAction(subject, later.Ref(),
			fmt.Sprintf("remove a copy of %s", anchor))}

	case same:
		f.Kind = KindRedundant
		f.Severity = SeverityMedium
		f.Title = fmt.Sprintf("%s is redundant under %s", subject, anchor)
		f.Detail = fmt.Sprintf(
			"Everything rule %s matches is already matched by %s above it, which takes the same action with the same inspection settings. %s never evaluates.",
			subject, anchor, subject)
		f.Actions = []Action{deleteAction(subject, later.Ref(),
			fmt.Sprintf("remove a rule %s already covers", anchor))}

	default:
		f.Kind = KindShadowed
		f.Severity = SeverityHigh
		f.Title = fmt.Sprintf("%s is shadowed by %s", subject, anchor)
		f.Detail = fmt.Sprintf(
			"Rule %s (%s) never evaluates: %s above it matches the same traffic and %s it instead. The policy %s expresses is not in force. Either %s belongs above %s, or it is obsolete and can go — but that is a decision about intent, so the only fix offered here is the promotion.",
			subject, later.Action, anchor, actionVerb(earlier.Action), subject, subject, anchor)
		// Deliberately the only action: a shadowed rule does something the
		// rule above it does not, and deleting it would erase a policy
		// somebody wrote on purpose along with the evidence that it is not
		// working. Redundant and duplicate rules are the ones this tool
		// removes; this one it reports.
		f.Actions = []Action{{
			Op:        OpMove,
			Target:    subject,
			TargetRef: later.Ref(),
			Position:  PositionBefore,
			Anchor:    anchor.Name,
			AnchorRef: earlier.Ref(),
			Reason:    fmt.Sprintf("put %s back in force, above the rule that shadows it", subject),
			Behaviour: BehaviourChanging,
		}}
	}
	f.ID = findingID(f.Kind, subject)
	return f
}

func actionVerb(action string) string {
	switch action {
	case ActionAccept:
		return "accepts"
	case ActionDrop:
		return "drops"
	case ActionReject:
		return "rejects"
	}
	return "handles"
}

// mergeFindings reports adjacent rule pairs that could be one rule.
//
// Adjacency is required: two rules that behave identically can only be
// collapsed when nothing evaluates between them, otherwise the merged rule
// would move traffic past a rule it used to reach.
func mergeFindings(rules []Rule, profiles []profile, opt Options) []Finding {
	if opt.skipped(KindMergeable) {
		return nil
	}
	var out []Finding
	for i := 0; i+1 < len(rules); i++ {
		a, b := &rules[i], &rules[i+1]
		if !a.IsEnabled() || !b.IsEnabled() {
			continue
		}
		if a.RuleType != RuleTypeFirewall || b.RuleType != RuleTypeFirewall {
			continue
		}
		// The union is only exact when neither side carries a condition
		// the merged rule could not reproduce.
		if !unrestricted(a) || !unrestricted(b) {
			continue
		}
		if !sameOutcome(a, b) {
			continue
		}
		// Unlike a removal, a merge produces a rule that really does match
		// both sides' traffic, so the logging setting has to agree too —
		// the survivor can only carry one.
		if a.IsLogging() != b.IsLogging() {
			continue
		}
		dim, ok := profiles[i].soleDifference(profiles[i+1])
		if !ok {
			continue
		}

		subject := refOf(b, i+1)
		anchor := refOf(a, i)
		f := Finding{
			Kind:     KindMergeable,
			Severity: SeverityLow,
			Subject:  subject,
			Related:  []RuleRef{anchor},
			Title:    fmt.Sprintf("%s and %s differ only in %s", anchor, subject, dim),
			Detail: fmt.Sprintf(
				"Rules %s and %s are adjacent, take the same action with the same settings, and differ only in their %s. Widening %s to cover both and deleting %s leaves the firewall matching exactly the same traffic with one rule fewer.",
				anchor, subject, dim, anchor, subject),
			Actions: []Action{{
				Op:        OpMerge,
				Target:    subject,
				TargetRef: b.Ref(),
				Anchor:    anchor.Name,
				AnchorRef: a.Ref(),
				Dimension: dim.String(),
				dim:       dim,
				intoIndex: i,
				fromIndex: i + 1,
				Reason:    fmt.Sprintf("fold %s into %s", subject, anchor),
				Behaviour: BehaviourPreserving,
			}},
		}
		f.ID = findingID(f.Kind, subject)
		out = append(out, f)
	}
	return out
}

// hygieneFindings reports per-rule problems that need no comparison.
func hygieneFindings(rules []Rule, profiles []profile, opt Options) []Finding {
	var out []Finding
	for i := range rules {
		r := &rules[i]
		ref := refOf(r, i)

		if !r.IsEnabled() {
			if !opt.skipped(KindDisabled) {
				f := Finding{
					Kind:     KindDisabled,
					Severity: SeverityInfo,
					Subject:  ref,
					Title:    fmt.Sprintf("%s is disabled", ref),
					Detail: fmt.Sprintf(
						"Rule %s is switched off. It costs nothing at runtime, but it is one more rule every reviewer has to read past.", ref),
					Actions: []Action{deleteAction(ref, r.Ref(), "remove a disabled rule")},
				}
				f.ID = findingID(f.Kind, ref)
				out = append(out, f)
			}
			continue
		}
		if r.RuleType != RuleTypeFirewall {
			continue
		}

		p := profiles[i]
		accept := r.Action == ActionAccept

		switch {
		case accept && p[dimSourceNetworks].any && p[dimDestinationNetworks].any && p[dimServices].any:
			if !opt.skipped(KindPermissive) {
				scope := "between the zones it names"
				if p[dimSourceZones].any && p[dimDestinationZones].any {
					scope = "between every zone on the firewall"
				}
				f := Finding{
					Kind:     KindPermissive,
					Severity: SeverityHigh,
					Subject:  ref,
					Title:    fmt.Sprintf("%s accepts any source to any destination on any service", ref),
					Detail: fmt.Sprintf(
						"Rule %s accepts traffic from any source to any destination on any service, %s. Every rule below it that is narrower than this is unreachable, and nothing here constrains what crosses.",
						ref, scope),
				}
				f.ID = findingID(f.Kind, ref)
				out = append(out, f)
			}

		case accept && p[dimServices].any:
			if !opt.skipped(KindBroadService) {
				f := Finding{
					Kind:     KindBroadService,
					Severity: SeverityLow,
					Subject:  ref,
					Title:    fmt.Sprintf("%s accepts any service", ref),
					Detail: fmt.Sprintf(
						"Rule %s constrains source and destination but accepts every service between them. Naming the services actually in use narrows the rule without changing what works.", ref),
				}
				f.ID = findingID(f.Kind, ref)
				out = append(out, f)
			}
		}

		if !r.IsLogging() && !opt.skipped(KindNoLogging) {
			f := Finding{
				Kind:     KindNoLogging,
				Severity: SeverityLow,
				Subject:  ref,
				Title:    fmt.Sprintf("%s does not log traffic", ref),
				Detail: fmt.Sprintf(
					"Rule %s has logTraffic off, so nothing it matches reaches the firewall log. Without that there is no way to tell whether the rule is still in use, and no audit trail behind it.", ref),
				Actions: []Action{{
					Op:        OpEnableLogging,
					Target:    ref,
					TargetRef: r.Ref(),
					Reason:    "turn on traffic logging",
					Behaviour: BehaviourPreserving,
				}},
			}
			f.ID = findingID(f.Kind, ref)
			out = append(out, f)
		}

		if accept && noInspection(r) && !opt.skipped(KindNoInspection) {
			f := Finding{
				Kind:     KindNoInspection,
				Severity: SeverityLow,
				Subject:  ref,
				Title:    fmt.Sprintf("%s accepts traffic with no security profile", ref),
				Detail: fmt.Sprintf(
					"Rule %s accepts traffic without an IPS policy, a web policy, an application policy, or any scanning. Traffic it matches passes uninspected.", ref),
			}
			f.ID = findingID(f.Kind, ref)
			out = append(out, f)
		}
	}
	return out
}

// noInspection reports whether an accept rule carries no security profile
// whatsoever.
func noInspection(r *Rule) bool {
	s := r.SecurityFeatures
	if s == nil {
		return true
	}
	return s.IPSPolicy == nil && s.WebPolicy == nil && s.ApplicationPolicy == nil &&
		!s.ScanHTTPAndDecryptedHTTPS && !s.ScanFTP && !s.ScanWithNDR &&
		!s.ZeroDayProtection && !s.DecryptHTTPSWebProxyMode
}

// suppressNoise drops the findings that only matter for rules which are
// staying. A rule the analyzer says is dead does not need its logging
// switched on first.
func suppressNoise(findings []Finding) []Finding {
	doomed := map[string]bool{}
	for _, f := range findings {
		switch f.Kind {
		case KindShadowed, KindRedundant, KindDuplicate, KindDisabled:
			doomed[f.Subject.Name] = true
		}
	}
	if len(doomed) == 0 {
		return findings
	}
	out := findings[:0]
	for _, f := range findings {
		switch f.Kind {
		case KindNoLogging, KindNoInspection, KindBroadService, KindPermissive, KindMergeable:
			if doomed[f.Subject.Name] {
				continue
			}
		}
		out = append(out, f)
	}
	return out
}

func findingID(k Kind, subject RuleRef) string {
	return fmt.Sprintf("%s/%d", k, subject.Position)
}
