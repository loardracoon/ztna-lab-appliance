package sophos

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// This file answers one question: does rule A match everything rule B
// matches? That is what makes a later rule dead, and every shadow,
// redundancy and duplicate finding rests on it.
//
// The API references networks, services and zones by *name*, not by
// address, so the analyzer compares sets of names rather than sets of
// addresses. That direction is safe: two rules referencing the same named
// object match the same traffic, so a name-set superset is always a real
// traffic superset. It is not complete — two differently named objects can
// hold the same subnet, and the analyzer will not notice — which is the
// error this tool wants to make. Missing a finding costs an admin nothing;
// inventing one costs them a rule.

// dimension is one normalized match dimension of a rule.
type dimension struct {
	// any is the API's {"any": true} branch — it matches everything.
	any bool
	// members holds "kind:name" entries, so that a country named "web" and
	// a service named "web" never compare equal.
	members map[string]struct{}
	// unknown marks a dimension the rule did not carry, or carried empty.
	// An unknown dimension neither covers nor is covered, which keeps a
	// partially populated rule from producing bogus findings.
	unknown bool
}

// covers reports whether d matches everything o matches.
func (d dimension) covers(o dimension) bool {
	if d.unknown || o.unknown {
		return false
	}
	if d.any {
		return true
	}
	if o.any {
		return false
	}
	for m := range o.members {
		if _, ok := d.members[m]; !ok {
			return false
		}
	}
	return true
}

// equal reports whether the two dimensions match exactly the same traffic.
func (d dimension) equal(o dimension) bool {
	if d.unknown || o.unknown {
		return false
	}
	if d.any != o.any {
		return false
	}
	if d.any {
		return true
	}
	if len(d.members) != len(o.members) {
		return false
	}
	return d.covers(o)
}

// union returns the dimension matching either d or o. It is only called
// once both dimensions are known.
func (d dimension) union(o dimension) dimension {
	if d.any || o.any {
		return dimension{any: true}
	}
	m := make(map[string]struct{}, len(d.members)+len(o.members))
	for k := range d.members {
		m[k] = struct{}{}
	}
	for k := range o.members {
		m[k] = struct{}{}
	}
	return dimension{members: m}
}

// addRefs folds a list of named references into the member set under kind.
// Names are compared verbatim apart from surrounding whitespace: the
// analyzer will not assume "LAN" and "lan" are the same object, because
// being wrong about that would mean recommending a delete on a rule that
// is still live.
func addRefs(m map[string]struct{}, kind string, refs []NamedRef) {
	for _, r := range refs {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			continue
		}
		m[kind+":"+name] = struct{}{}
	}
}

func finish(m map[string]struct{}) dimension {
	if len(m) == 0 {
		return dimension{unknown: true}
	}
	return dimension{members: m}
}

func zoneDim(z *ZoneSet) dimension {
	if z == nil {
		return dimension{unknown: true}
	}
	if z.Any {
		return dimension{any: true}
	}
	m := map[string]struct{}{}
	addRefs(m, "zone", z.Zones)
	return finish(m)
}

func networkDim(n *NetworkSet) dimension {
	if n == nil {
		return dimension{unknown: true}
	}
	if n.Any {
		return dimension{any: true}
	}
	m := map[string]struct{}{}
	addRefs(m, "country", n.Countries)
	addRefs(m, "countryGroup", n.CountryGroups)
	addRefs(m, "fqdn", n.FQDNAddresses)
	addRefs(m, "fqdnGroup", n.FQDNGroups)
	addRefs(m, "ipv4", n.IPv4Addresses)
	addRefs(m, "ipv4Group", n.IPv4Groups)
	addRefs(m, "mac", n.MACAddresses)
	return finish(m)
}

func serviceDim(s *ServiceSet) dimension {
	if s == nil {
		return dimension{unknown: true}
	}
	if s.Any {
		return dimension{any: true}
	}
	m := map[string]struct{}{}
	addRefs(m, "service", s.Services)
	addRefs(m, "serviceGroup", s.ServiceGroups)
	return finish(m)
}

// dimKey names each match dimension, for findings that have to say which
// one differs.
type dimKey int

const (
	dimSourceZones dimKey = iota
	dimDestinationZones
	dimSourceNetworks
	dimDestinationNetworks
	dimServices
)

func (k dimKey) String() string {
	switch k {
	case dimSourceZones:
		return "source zones"
	case dimDestinationZones:
		return "destination zones"
	case dimSourceNetworks:
		return "source networks"
	case dimDestinationNetworks:
		return "destination networks"
	case dimServices:
		return "services"
	}
	return "unknown"
}

var allDims = []dimKey{dimSourceZones, dimDestinationZones, dimSourceNetworks, dimDestinationNetworks, dimServices}

// profile is a rule's five match dimensions, normalized.
type profile [5]dimension

func profileOf(r *Rule) profile {
	return profile{
		dimSourceZones:         zoneDim(r.SourceZones),
		dimDestinationZones:    zoneDim(r.DestinationZones),
		dimSourceNetworks:      networkDim(r.SourceNetworks),
		dimDestinationNetworks: networkDim(r.DestinationNetworks),
		dimServices:            serviceDim(r.ServicesOrGroups),
	}
}

// complete reports whether every dimension is known. An incomplete profile
// takes no part in coverage analysis.
func (p profile) complete() bool {
	for _, d := range p {
		if d.unknown {
			return false
		}
	}
	return true
}

// covers reports whether p matches everything q matches.
func (p profile) covers(q profile) bool {
	if !p.complete() || !q.complete() {
		return false
	}
	for _, k := range allDims {
		if !p[k].covers(q[k]) {
			return false
		}
	}
	return true
}

// equal reports whether the two profiles match exactly the same traffic.
func (p profile) equal(q profile) bool {
	if !p.complete() || !q.complete() {
		return false
	}
	for _, k := range allDims {
		if !p[k].equal(q[k]) {
			return false
		}
	}
	return true
}

// soleDifference returns the one dimension on which p and q differ, when
// there is exactly one and neither side covers the other on it. That is
// the shape of a mergeable pair: same traffic treatment, disjoint slices
// of one dimension. When the differing dimension *is* comparable the pair
// is a redundancy instead, and coverage analysis reports it.
func (p profile) soleDifference(q profile) (dimKey, bool) {
	if !p.complete() || !q.complete() {
		return 0, false
	}
	diff := -1
	for _, k := range allDims {
		if p[k].equal(q[k]) {
			continue
		}
		if diff >= 0 {
			return 0, false
		}
		diff = int(k)
	}
	if diff < 0 {
		return 0, false
	}
	k := dimKey(diff)
	if p[k].covers(q[k]) || q[k].covers(p[k]) {
		return 0, false
	}
	return k, true
}

// ---------- rule-level guards ----------

// unrestricted reports whether a rule carries no narrowing condition
// beyond its five match dimensions. Only an unrestricted rule can be
// claimed to cover another one: a schedule, an exclusion, a user match or
// a heartbeat requirement all make a rule match *less* than its dimensions
// suggest, and the analyzer cannot reason about the overlap.
func unrestricted(r *Rule) bool {
	return emptyExclusions(r.Exclusions) &&
		r.Schedule == nil &&
		anyUsers(r.UserAuthentication) &&
		noHeartbeatRequirement(r.Heartbeat)
}

// coverageAllowed reports whether earlier may be claimed to cover later.
// Where earlier carries a narrowing condition, the pair still qualifies if
// later carries exactly the same one — identical schedules, identical user
// matches and identical heartbeat requirements cancel out.
func coverageAllowed(earlier, later *Rule) bool {
	if earlier.RuleType != RuleTypeFirewall || later.RuleType != RuleTypeFirewall {
		return false
	}
	if !earlier.IsEnabled() {
		return false
	}
	// An exclusion on the earlier rule punches holes the analyzer cannot
	// measure. One on the later rule only shrinks it, which is harmless.
	if !emptyExclusions(earlier.Exclusions) {
		return false
	}
	if !sameRef(earlier.Schedule, later.Schedule) && earlier.Schedule != nil {
		return false
	}
	if !anyUsers(earlier.UserAuthentication) && !sameJSON(earlier.UserAuthentication, later.UserAuthentication) {
		return false
	}
	if !noHeartbeatRequirement(earlier.Heartbeat) && !sameJSON(earlier.Heartbeat, later.Heartbeat) {
		return false
	}
	return true
}

// sameOutcome reports whether two rules treat the traffic they match
// identically — same action, same inspection. Two rules with the same
// outcome are interchangeable, so a covered one can simply go; where the
// outcomes differ, the covered rule was written to do something the
// covering rule does not, and that intent is silently not in force.
//
// Logging is deliberately not part of this. A covered rule never matches a
// packet, so it never logs either, and removing it loses nothing that was
// happening. Whether a rule logs is a finding in its own right.
func sameOutcome(a, b *Rule) bool {
	if a.Action != b.Action {
		return false
	}
	// drop and reject discard the inspection profile server-side, so there
	// is nothing else to compare.
	if a.Action != ActionAccept {
		return true
	}
	return sameJSON(a.SecurityFeatures, b.SecurityFeatures) &&
		sameJSON(a.EmailScanning, b.EmailScanning) &&
		sameJSON(a.QoS, b.QoS) &&
		sameJSON(a.Heartbeat, b.Heartbeat)
}

func emptyExclusions(e *Exclusions) bool {
	if e == nil {
		return true
	}
	return zoneDim(e.SourceZones).unknown &&
		zoneDim(e.DestinationZones).unknown &&
		networkDim(e.SourceNetworks).unknown &&
		networkDim(e.DestinationNetworks).unknown &&
		serviceDim(e.ServicesOrGroups).unknown
}

func anyUsers(u *UserAuthentication) bool {
	if u == nil || u.UsersOrGroups == nil {
		return true
	}
	g := u.UsersOrGroups
	if g.Any {
		return true
	}
	return len(g.Users) == 0 && len(g.UserGroups) == 0
}

func noHeartbeatRequirement(h *Heartbeat) bool {
	if h == nil {
		return true
	}
	return endpointUnrestricted(h.Source) && endpointUnrestricted(h.Destination)
}

func endpointUnrestricted(e *HeartbeatEndpoint) bool {
	if e == nil {
		return true
	}
	if e.BlockClientsWithNoHeartbeat {
		return false
	}
	return e.MinimumLevel == "" || e.MinimumLevel == "noRestriction"
}

func sameRef(a, b *NamedRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return strings.TrimSpace(a.Name) == strings.TrimSpace(b.Name)
}

// sameJSON compares two optional sub-objects structurally. Both sides have
// already been through normalizeRules, so slice order is not a factor.
func sameJSON(a, b any) bool {
	if isNil(a) && isNil(b) {
		return true
	}
	if isNil(a) != isNil(b) {
		// An absent object and one carrying only zero values describe the
		// same configuration, so compare the encodings rather than the
		// pointers.
		return string(mustJSON(a)) == string(mustJSON(b))
	}
	return reflect.DeepEqual(a, b)
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

func mustJSON(v any) []byte {
	if isNil(v) {
		return []byte("{}")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	if string(b) == "null" {
		return []byte("{}")
	}
	return b
}

// normalizeRules returns a deep copy of rules with every reference list
// sorted, so that structural comparison does not trip over ordering the
// firewall never promised.
func normalizeRules(rules []Rule) []Rule {
	out := make([]Rule, len(rules))
	for i := range rules {
		buf, err := json.Marshal(&rules[i])
		if err != nil {
			out[i] = rules[i]
			continue
		}
		if err := json.Unmarshal(buf, &out[i]); err != nil {
			out[i] = rules[i]
			continue
		}
		sortRule(&out[i])
	}
	return out
}

func sortRule(r *Rule) {
	sortZones(r.SourceZones)
	sortZones(r.DestinationZones)
	sortNetworks(r.SourceNetworks)
	sortNetworks(r.DestinationNetworks)
	sortServices(r.ServicesOrGroups)
	if r.UserAuthentication != nil && r.UserAuthentication.UsersOrGroups != nil {
		sortRefs(r.UserAuthentication.UsersOrGroups.Users)
		sortRefs(r.UserAuthentication.UsersOrGroups.UserGroups)
	}
	if e := r.Exclusions; e != nil {
		sortZones(e.SourceZones)
		sortZones(e.DestinationZones)
		sortNetworks(e.SourceNetworks)
		sortNetworks(e.DestinationNetworks)
		sortServices(e.ServicesOrGroups)
	}
}

func sortZones(z *ZoneSet) {
	if z != nil {
		sortRefs(z.Zones)
	}
}

func sortNetworks(n *NetworkSet) {
	if n == nil {
		return
	}
	sortRefs(n.Countries)
	sortRefs(n.CountryGroups)
	sortRefs(n.FQDNAddresses)
	sortRefs(n.FQDNGroups)
	sortRefs(n.IPv4Addresses)
	sortRefs(n.IPv4Groups)
	sortRefs(n.MACAddresses)
}

func sortServices(s *ServiceSet) {
	if s == nil {
		return
	}
	sortRefs(s.Services)
	sortRefs(s.ServiceGroups)
}

func sortRefs(refs []NamedRef) {
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
}
