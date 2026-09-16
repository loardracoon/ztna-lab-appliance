package sophos

import (
	"strings"
	"testing"
)

// ---------- helpers ----------

func refs(names ...string) []NamedRef {
	out := make([]NamedRef, 0, len(names))
	for _, n := range names {
		out = append(out, NamedRef{Name: n})
	}
	return out
}

// rule builds a firewall-type rule. Every match dimension is populated,
// because a rule missing one is deliberately excluded from coverage
// analysis and would make these tests pass for the wrong reason.
func rule(name, action string, opts ...func(*Rule)) Rule {
	r := Rule{
		ID:                  "id-" + name,
		Name:                name,
		RuleType:            RuleTypeFirewall,
		Action:              action,
		Enabled:             Bool(true),
		LogTraffic:          Bool(true),
		SourceZones:         &ZoneSet{Zones: refs("lan")},
		DestinationZones:    &ZoneSet{Zones: refs("wan")},
		SourceNetworks:      &NetworkSet{Any: true},
		DestinationNetworks: &NetworkSet{Any: true},
		ServicesOrGroups:    &ServiceSet{Services: refs("https")},
		SecurityFeatures:    &SecurityFeatures{IPSPolicy: &NamedRef{Name: "lan-protection"}},
	}
	for _, o := range opts {
		o(&r)
	}
	return r
}

func srcNets(names ...string) func(*Rule) {
	return func(r *Rule) { r.SourceNetworks = &NetworkSet{IPv4Groups: refs(names...)} }
}
func dstNets(names ...string) func(*Rule) {
	return func(r *Rule) { r.DestinationNetworks = &NetworkSet{IPv4Groups: refs(names...)} }
}
func services(names ...string) func(*Rule) {
	return func(r *Rule) { r.ServicesOrGroups = &ServiceSet{Services: refs(names...)} }
}
func anyServices(r *Rule) { r.ServicesOrGroups = &ServiceSet{Any: true} }
func anySrcNets(r *Rule)  { r.SourceNetworks = &NetworkSet{Any: true} }
func anyDstNets(r *Rule)  { r.DestinationNetworks = &NetworkSet{Any: true} }
func disabled(r *Rule)    { r.Enabled = Bool(false) }
func noLog(r *Rule)       { r.LogTraffic = Bool(false) }
func noProfile(r *Rule)   { r.SecurityFeatures = nil }
func anyZones(r *Rule) {
	r.SourceZones = &ZoneSet{Any: true}
	r.DestinationZones = &ZoneSet{Any: true}
}
func schedule(name string) func(*Rule) {
	return func(r *Rule) { r.Schedule = &NamedRef{Name: name} }
}

// analyze runs every check and keeps the whole result, so that a test can
// assert on what was *not* reported as easily as on what was.
func analyze(t *testing.T, rules ...Rule) *Report {
	t.Helper()
	return Analyze(rules, Options{Source: "test"})
}

func findingFor(rep *Report, kind Kind, name string) *Finding {
	for i := range rep.Findings {
		if rep.Findings[i].Kind == kind && rep.Findings[i].Subject.Name == name {
			return &rep.Findings[i]
		}
	}
	return nil
}

func mustFind(t *testing.T, rep *Report, kind Kind, name string) *Finding {
	t.Helper()
	f := findingFor(rep, kind, name)
	if f == nil {
		t.Fatalf("expected a %s finding for %q; got %s", kind, name, summarize(rep))
	}
	return f
}

func mustNotFind(t *testing.T, rep *Report, kind Kind, name string) {
	t.Helper()
	if f := findingFor(rep, kind, name); f != nil {
		t.Fatalf("unexpected %s finding for %q: %s", kind, name, f.Title)
	}
}

func summarize(rep *Report) string {
	var b strings.Builder
	b.WriteString("findings[")
	for i, f := range rep.Findings {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(string(f.Kind) + ":" + f.Subject.Name)
	}
	b.WriteString("]")
	return b.String()
}

// ---------- coverage ----------

func TestShadowedRuleIsReported(t *testing.T) {
	rep := analyze(t,
		rule("accept-all-web", ActionAccept, anyServices),
		rule("block-telnet", ActionDrop, services("telnet")),
	)

	f := mustFind(t, rep, KindShadowed, "block-telnet")
	if f.Severity != SeverityHigh {
		t.Errorf("shadowed rule severity = %s, want high", f.Severity)
	}
	if len(f.Related) != 1 || f.Related[0].Name != "accept-all-web" {
		t.Errorf("related = %v, want the shadowing rule", f.Related)
	}
	// Promotion is the only automatic remedy. A shadowed rule expresses a
	// policy the rule above it does not, so deleting it would erase both
	// that policy and the evidence that it is not working — the tool
	// reports it and leaves the call to the admin.
	if len(f.Actions) != 1 || f.Actions[0].Op != OpMove {
		t.Fatalf("actions = %+v, want a single move", f.Actions)
	}
	if got := f.Actions[0].Position; got != PositionBefore {
		t.Errorf("move position = %q, want before", got)
	}
	if f.Actions[0].Behaviour != BehaviourChanging {
		t.Errorf("a move must be flagged as changing behaviour, got %s", f.Actions[0].Behaviour)
	}
}

func TestRedundantRuleIsReportedWhenOutcomesMatch(t *testing.T) {
	rep := analyze(t,
		rule("web-broad", ActionAccept, services("http", "https", "dns")),
		rule("web-narrow", ActionAccept, services("https")),
	)

	f := mustFind(t, rep, KindRedundant, "web-narrow")
	if f.Actions[0].Op != OpDelete || f.Actions[0].Behaviour != BehaviourPreserving {
		t.Errorf("actions = %+v, want a behaviour-preserving delete", f.Actions)
	}
	mustNotFind(t, rep, KindShadowed, "web-narrow")
}

func TestCoveredRuleWithDifferentInspectionIsShadowedNotRedundant(t *testing.T) {
	// Same action, but the narrower rule adds scanning the broad one does
	// not do. Deleting it would drop that inspection, so it is not a
	// redundancy — the admin wrote it for a reason that is not in force.
	strict := func(r *Rule) {
		r.SecurityFeatures = &SecurityFeatures{
			IPSPolicy:                 &NamedRef{Name: "lan-protection"},
			ScanHTTPAndDecryptedHTTPS: true,
			ZeroDayProtection:         true,
		}
	}
	rep := analyze(t,
		rule("web-broad", ActionAccept, services("http", "https")),
		rule("web-scanned", ActionAccept, services("https"), strict),
	)

	mustFind(t, rep, KindShadowed, "web-scanned")
	mustNotFind(t, rep, KindRedundant, "web-scanned")
}

func TestDuplicateRuleIsReported(t *testing.T) {
	rep := analyze(t,
		rule("allow-https", ActionAccept),
		rule("allow-https-copy", ActionAccept),
	)

	f := mustFind(t, rep, KindDuplicate, "allow-https-copy")
	if f.Actions[0].Op != OpDelete {
		t.Errorf("actions = %+v, want a delete", f.Actions)
	}
	mustNotFind(t, rep, KindRedundant, "allow-https-copy")
}

func TestOnlyTheFirstCoveringRuleIsReported(t *testing.T) {
	rep := analyze(t,
		rule("catch-all", ActionAccept, anyServices),
		rule("also-broad", ActionAccept, anyServices),
		rule("narrow", ActionAccept, services("https")),
	)

	f := mustFind(t, rep, KindRedundant, "narrow")
	if f.Related[0].Name != "catch-all" {
		t.Errorf("related = %s, want the topmost covering rule", f.Related[0].Name)
	}
	if f.Related[0].Position != 1 {
		t.Errorf("related position = %d, want 1", f.Related[0].Position)
	}
}

func TestDisabledRuleShadowsNothing(t *testing.T) {
	rep := analyze(t,
		rule("accept-all", ActionAccept, anyServices, disabled),
		rule("block-telnet", ActionDrop, services("telnet")),
	)
	mustNotFind(t, rep, KindShadowed, "block-telnet")
	mustFind(t, rep, KindDisabled, "accept-all")
}

func TestScheduledRuleDoesNotShadowAnUnscheduledOne(t *testing.T) {
	// The broad rule only applies during business hours, so it cannot
	// account for every packet the narrow rule would see.
	rep := analyze(t,
		rule("accept-all-workhours", ActionAccept, anyServices, schedule("work-hours")),
		rule("block-telnet", ActionDrop, services("telnet")),
	)
	mustNotFind(t, rep, KindShadowed, "block-telnet")
}

func TestIdenticalSchedulesStillShadow(t *testing.T) {
	rep := analyze(t,
		rule("accept-all-workhours", ActionAccept, anyServices, schedule("work-hours")),
		rule("block-telnet", ActionDrop, services("telnet"), schedule("work-hours")),
	)
	mustFind(t, rep, KindShadowed, "block-telnet")
}

func TestExclusionOnTheCoveringRuleBlocksTheClaim(t *testing.T) {
	withExclusion := func(r *Rule) {
		r.Exclusions = &Exclusions{ServicesOrGroups: &ServiceSet{Services: refs("telnet")}}
	}
	rep := analyze(t,
		rule("accept-all-but-telnet", ActionAccept, anyServices, withExclusion),
		rule("block-telnet", ActionDrop, services("telnet")),
	)
	mustNotFind(t, rep, KindShadowed, "block-telnet")
}

func TestUserRestrictedRuleDoesNotShadow(t *testing.T) {
	forGroup := func(r *Rule) {
		r.UserAuthentication = &UserAuthentication{
			UsersOrGroups: &UsersOrGroups{UserGroups: refs("contractors")},
		}
	}
	rep := analyze(t,
		rule("accept-all-contractors", ActionAccept, anyServices, forGroup),
		rule("block-telnet", ActionDrop, services("telnet")),
	)
	mustNotFind(t, rep, KindShadowed, "block-telnet")
}

func TestHeartbeatRequirementBlocksTheClaim(t *testing.T) {
	needsHeartbeat := func(r *Rule) {
		r.Heartbeat = &Heartbeat{Source: &HeartbeatEndpoint{
			BlockClientsWithNoHeartbeat: true, MinimumLevel: "green",
		}}
	}
	rep := analyze(t,
		rule("accept-healthy-endpoints", ActionAccept, anyServices, needsHeartbeat),
		rule("block-telnet", ActionDrop, services("telnet")),
	)
	mustNotFind(t, rep, KindShadowed, "block-telnet")
}

func TestPartialOverlapIsNotShadowing(t *testing.T) {
	rep := analyze(t,
		rule("dc-web", ActionAccept, srcNets("branch-a"), services("https")),
		rule("branch-web", ActionAccept, srcNets("branch-b"), services("https")),
	)
	mustNotFind(t, rep, KindShadowed, "branch-web")
	mustNotFind(t, rep, KindRedundant, "branch-web")
}

func TestUnnamedObjectsNeverCoverEachOther(t *testing.T) {
	// Two rules with the same subnet under different object names must not
	// be collapsed: the analyzer compares names, and guessing would risk
	// recommending the deletion of a live rule.
	rep := analyze(t,
		rule("hq-web", ActionAccept, srcNets("hq-subnets"), services("https")),
		rule("hq-web-alias", ActionAccept, srcNets("hq-networks"), services("https")),
	)
	mustNotFind(t, rep, KindRedundant, "hq-web-alias")
	mustNotFind(t, rep, KindDuplicate, "hq-web-alias")
}

func TestIncompleteRuleIsSkippedByCoverage(t *testing.T) {
	missing := rule("accept-all", ActionAccept, anyServices)
	missing.DestinationZones = nil
	rep := analyze(t, missing, rule("block-telnet", ActionDrop, services("telnet")))
	mustNotFind(t, rep, KindShadowed, "block-telnet")
}

func TestWAFRulesAreLeftOutOfCoverage(t *testing.T) {
	waf := rule("web-app", ActionAccept, anyServices)
	waf.RuleType = RuleTypeWAF
	rep := analyze(t, waf, rule("block-telnet", ActionDrop, services("telnet")))
	mustNotFind(t, rep, KindShadowed, "block-telnet")
}

// ---------- merge ----------

func TestAdjacentRulesDifferingInOneDimensionAreMergeable(t *testing.T) {
	rep := analyze(t,
		rule("web-a", ActionAccept, noProfile, srcNets("branch-a"), services("https")),
		rule("web-b", ActionAccept, noProfile, srcNets("branch-b"), services("https")),
	)

	f := mustFind(t, rep, KindMergeable, "web-b")
	if f.Actions[0].Op != OpMerge {
		t.Fatalf("actions = %+v, want a merge", f.Actions)
	}
	if f.Actions[0].Dimension != "source networks" {
		t.Errorf("dimension = %q, want source networks", f.Actions[0].Dimension)
	}
	if f.Actions[0].Behaviour != BehaviourPreserving {
		t.Errorf("merging two adjacent identical-outcome rules preserves behaviour, got %s", f.Actions[0].Behaviour)
	}
}

func TestNonAdjacentRulesAreNotMergeable(t *testing.T) {
	rep := analyze(t,
		rule("web-a", ActionAccept, srcNets("branch-a"), services("https")),
		rule("block-ssh", ActionDrop, srcNets("branch-a"), services("ssh")),
		rule("web-b", ActionAccept, srcNets("branch-b"), services("https")),
	)
	mustNotFind(t, rep, KindMergeable, "web-b")
}

func TestRulesDifferingInTwoDimensionsAreNotMergeable(t *testing.T) {
	rep := analyze(t,
		rule("a", ActionAccept, srcNets("branch-a"), services("https")),
		rule("b", ActionAccept, srcNets("branch-b"), services("ssh")),
	)
	mustNotFind(t, rep, KindMergeable, "b")
}

func TestRulesWithDifferentActionsAreNotMergeable(t *testing.T) {
	rep := analyze(t,
		rule("a", ActionAccept, srcNets("branch-a"), services("https")),
		rule("b", ActionDrop, srcNets("branch-b"), services("https")),
	)
	mustNotFind(t, rep, KindMergeable, "b")
}

// ---------- hygiene ----------

func TestAnyToAnyAcceptIsReported(t *testing.T) {
	rep := analyze(t, rule("wide-open", ActionAccept, anySrcNets, anyDstNets, anyServices, anyZones))

	f := mustFind(t, rep, KindPermissive, "wide-open")
	if f.Severity != SeverityHigh {
		t.Errorf("severity = %s, want high", f.Severity)
	}
	if !strings.Contains(f.Detail, "every zone") {
		t.Errorf("detail should call out the zone scope, got %q", f.Detail)
	}
	// An any/any/any rule is already the headline; do not also nag about
	// its service breadth.
	mustNotFind(t, rep, KindBroadService, "wide-open")
}

func TestBroadServiceIsReportedSeparately(t *testing.T) {
	rep := analyze(t, rule("lan-to-dmz", ActionAccept, srcNets("lan"), dstNets("dmz"), anyServices))
	mustFind(t, rep, KindBroadService, "lan-to-dmz")
	mustNotFind(t, rep, KindPermissive, "lan-to-dmz")
}

func TestUnloggedRuleIsReportedWithAFix(t *testing.T) {
	rep := analyze(t, rule("quiet", ActionAccept, noLog))

	f := mustFind(t, rep, KindNoLogging, "quiet")
	if f.Actions[0].Op != OpEnableLogging || f.Actions[0].Behaviour != BehaviourPreserving {
		t.Errorf("actions = %+v, want a behaviour-preserving log fix", f.Actions)
	}
	if f.Actions[0].TargetRef != "id-quiet" {
		t.Errorf("target ref = %q, want the rule ID", f.Actions[0].TargetRef)
	}
}

func TestAcceptWithoutASecurityProfileIsReported(t *testing.T) {
	rep := analyze(t, rule("bare", ActionAccept, noProfile))
	mustFind(t, rep, KindNoInspection, "bare")
}

func TestDropRuleNeedsNoSecurityProfile(t *testing.T) {
	rep := analyze(t, rule("block", ActionDrop, noProfile))
	mustNotFind(t, rep, KindNoInspection, "block")
}

func TestDeadRulesDoNotAlsoCollectHygieneFindings(t *testing.T) {
	// "narrow" is redundant, so telling the admin to switch its logging on
	// is noise — it is on its way out.
	rep := analyze(t,
		rule("broad", ActionAccept, anyServices),
		rule("narrow", ActionAccept, services("https"), noLog),
	)
	mustFind(t, rep, KindRedundant, "narrow")
	mustNotFind(t, rep, KindNoLogging, "narrow")
}

func TestDisabledRuleIsReportedAsInfo(t *testing.T) {
	rep := analyze(t, rule("old", ActionAccept, disabled))
	f := mustFind(t, rep, KindDisabled, "old")
	if f.Severity != SeverityInfo {
		t.Errorf("severity = %s, want info", f.Severity)
	}
}

// ---------- report plumbing ----------

func TestFindingsAreSortedBySeverityThenPosition(t *testing.T) {
	rep := analyze(t,
		rule("quiet", ActionAccept, noLog, services("https")),
		rule("accept-all", ActionAccept, anyServices),
		rule("block-telnet", ActionDrop, services("telnet")),
	)
	if len(rep.Findings) < 2 {
		t.Fatalf("expected several findings, got %s", summarize(rep))
	}
	for i := 1; i < len(rep.Findings); i++ {
		prev, cur := rep.Findings[i-1], rep.Findings[i]
		if prev.Severity < cur.Severity {
			t.Fatalf("findings out of order at %d: %s before %s", i, prev.Severity, cur.Severity)
		}
		if prev.Severity == cur.Severity && prev.Subject.Position > cur.Subject.Position {
			t.Fatalf("equal severities not ordered by position at %d", i)
		}
	}
}

func TestMinSeverityFiltersFindings(t *testing.T) {
	rules := []Rule{rule("quiet", ActionAccept, noLog), rule("old", ActionAccept, disabled)}

	all := Analyze(rules, Options{})
	if all.Count(KindNoLogging) == 0 || all.Count(KindDisabled) == 0 {
		t.Fatalf("baseline missing findings: %s", summarize(all))
	}
	high := Analyze(rules, Options{MinSeverity: SeverityMedium})
	if len(high.Findings) != 0 {
		t.Errorf("min-severity medium kept %s", summarize(high))
	}
}

func TestSkipRemovesACheck(t *testing.T) {
	rules := []Rule{rule("quiet", ActionAccept, noLog)}
	rep := Analyze(rules, Options{Skip: []Kind{KindNoLogging}})
	mustNotFind(t, rep, KindNoLogging, "quiet")
}

func TestEnabledCountAndPositions(t *testing.T) {
	rep := analyze(t,
		rule("a", ActionAccept, services("https")),
		rule("b", ActionAccept, services("ssh"), disabled),
		rule("c", ActionAccept, services("dns")),
	)
	if rep.Rules != 3 || rep.Enabled != 2 {
		t.Errorf("rules=%d enabled=%d, want 3 and 2", rep.Rules, rep.Enabled)
	}
	f := mustFind(t, rep, KindDisabled, "b")
	if f.Subject.Position != 2 {
		t.Errorf("position = %d, want 2 (1-based evaluation order)", f.Subject.Position)
	}
}

func TestAnalyzeDoesNotMutateTheInput(t *testing.T) {
	rules := []Rule{rule("a", ActionAccept, services("https", "http", "dns"))}
	before := rules[0].ServicesOrGroups.Services[0].Name
	Analyze(rules, Options{})
	if got := rules[0].ServicesOrGroups.Services[0].Name; got != before {
		t.Errorf("Analyze reordered the caller's slice: %q became %q", before, got)
	}
}

func TestSeverityParsing(t *testing.T) {
	for in, want := range map[string]Severity{
		"":       SeverityInfo,
		"info":   SeverityInfo,
		"LOW":    SeverityLow,
		" med ":  SeverityMedium,
		"medium": SeverityMedium,
		"high":   SeverityHigh,
	} {
		got, err := ParseSeverity(in)
		if err != nil {
			t.Errorf("ParseSeverity(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSeverity(%q) = %s, want %s", in, got, want)
		}
	}
	if _, err := ParseSeverity("critical"); err == nil {
		t.Error("ParseSeverity(critical) should fail")
	}
}

func TestRulesWithDifferentLoggingAreNotMergeable(t *testing.T) {
	// A merge leaves one rule carrying one logging setting, so a pair that
	// disagrees about logging cannot be folded together without changing
	// what reaches the log.
	rep := analyze(t,
		rule("a", ActionAccept, noProfile, srcNets("branch-a"), services("https")),
		rule("b", ActionAccept, noProfile, srcNets("branch-b"), services("https"), noLog),
	)
	mustNotFind(t, rep, KindMergeable, "b")
}
