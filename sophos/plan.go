package sophos

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Op is one change the tool can make through the API.
type Op string

// The operations a plan can carry.
const (
	OpEnableLogging Op = "log"     // PATCH logTraffic=true
	OpDisable       Op = "disable" // PATCH enabled=false
	OpMove          Op = "move"    // POST /move
	OpMerge         Op = "merge"   // PATCH the survivor, DELETE the absorbed rule
	OpDelete        Op = "delete"  // DELETE the rule
)

// AllOps lists every operation, for flag parsing and help text.
var AllOps = []Op{OpEnableLogging, OpDisable, OpMove, OpMerge, OpDelete}

// ParseOps turns a comma-separated flag value into an allow set. "all"
// enables everything; an empty string allows nothing.
func ParseOps(s string) (map[Op]bool, error) {
	allow := map[Op]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		switch part {
		case "":
			continue
		case "all":
			for _, op := range AllOps {
				allow[op] = true
			}
		case "none":
			return map[Op]bool{}, nil
		default:
			ok := false
			for _, op := range AllOps {
				if Op(part) == op {
					allow[op] = true
					ok = true
				}
			}
			if !ok {
				return nil, fmt.Errorf("sophos: unknown operation %q (want any of %s, or all/none)", part, opNames())
			}
		}
	}
	return allow, nil
}

func opNames() string {
	names := make([]string, len(AllOps))
	for i, op := range AllOps {
		names[i] = string(op)
	}
	return strings.Join(names, ", ")
}

// Behaviour says whether applying an action can change what the firewall
// forwards. Removing a rule that never evaluates is behaviour-preserving;
// reordering rules is not.
type Behaviour string

// Behaviour classes.
const (
	BehaviourPreserving Behaviour = "preserves-behaviour"
	BehaviourChanging   Behaviour = "changes-behaviour"
)

// Action is one proposed change.
//
// The actions on a single Finding are alternatives, not a sequence: a
// shadowed rule can be promoted above the rule hiding it *or* deleted, and
// the planner picks whichever one the operator allowed, never both.
type Action struct {
	Op        Op        `json:"op"`
	Target    RuleRef   `json:"target"`
	TargetRef string    `json:"targetRef,omitempty"` // {idOrName} for the request path
	Position  string    `json:"position,omitempty"`
	Anchor    string    `json:"anchor,omitempty"` // anchor rule, by name
	AnchorRef string    `json:"anchorRef,omitempty"`
	Dimension string    `json:"dimension,omitempty"`
	Reason    string    `json:"reason"`
	Behaviour Behaviour `json:"behaviour"`

	// Merge bookkeeping, kept out of the JSON: which dimension to union
	// and which two rules in the analyzed base to union it from.
	dim       dimKey
	intoIndex int
	fromIndex int
}

// Describe renders the action as the API call it will make.
func (a Action) Describe() string {
	switch a.Op {
	case OpEnableLogging:
		return fmt.Sprintf("PATCH %s  {\"logTraffic\": true}", a.Target)
	case OpDisable:
		return fmt.Sprintf("PATCH %s  {\"enabled\": false}", a.Target)
	case OpMove:
		return fmt.Sprintf("MOVE  %s  %s %q", a.Target, a.Position, a.Anchor)
	case OpMerge:
		return fmt.Sprintf("MERGE %s into %q (union of %s), then DELETE %s", a.Target, a.Anchor, a.Dimension, a.Target)
	case OpDelete:
		return fmt.Sprintf("DELETE %s", a.Target)
	}
	return string(a.Op)
}

func deleteAction(ref RuleRef, idOrName, reason string) Action {
	return Action{
		Op:        OpDelete,
		Target:    ref,
		TargetRef: idOrName,
		Reason:    reason,
		Behaviour: BehaviourPreserving,
	}
}

// PlanOptions controls which of a report's proposed actions make it into
// an executable plan.
type PlanOptions struct {
	// Allow is the set of operations the operator opted into. Anything
	// outside it is left out of the plan.
	Allow map[Op]bool
	// PreferDisable rewrites every delete into a disable, so a cleanup can
	// be reviewed on the firewall — and reversed — before anything is
	// actually removed. The rewritten action still has to pass Allow.
	PreferDisable bool
}

// Plan is an ordered, conflict-free list of API calls.
type Plan struct {
	Actions []Action `json:"actions"`
	// Skipped records findings that had a remedy the operator did not
	// allow, so the report can say what -allow would unlock.
	Skipped map[Op]int `json:"skipped,omitempty"`

	rules []Rule
}

// opRank fixes execution order. Logging first because it touches nothing
// else; merges and moves next, while every rule they name still exists;
// removals last.
var opRank = map[Op]int{
	OpEnableLogging: 0,
	OpMerge:         1,
	OpMove:          2,
	OpDisable:       3,
	OpDelete:        4,
}

// BuildPlan turns a report into the calls to make.
func BuildPlan(rep *Report, opt PlanOptions) *Plan {
	plan := &Plan{Skipped: map[Op]int{}, rules: rep.rules}

	// removed tracks rules the plan takes out of service, so that nothing
	// later in the plan tries to patch them or anchor a move to them.
	removed := map[string]bool{}
	claimed := map[string]bool{} // rules already targeted by some action

	var chosen []Action
	for _, f := range rep.Findings {
		act, ok := pick(f.Actions, opt)
		if !ok {
			for _, a := range f.Actions {
				plan.Skipped[a.Op]++
			}
			continue
		}
		if claimed[act.Target.Name] {
			continue
		}
		claimed[act.Target.Name] = true
		if act.Op == OpDelete || act.Op == OpDisable || act.Op == OpMerge {
			removed[act.Target.Name] = true
		}
		chosen = append(chosen, act)
	}

	// Drop anything that depends on a rule another action removes.
	for _, act := range chosen {
		switch act.Op {
		case OpMove:
			if removed[act.Anchor] {
				continue
			}
		case OpMerge:
			if removed[act.Anchor] {
				continue
			}
		}
		plan.Actions = append(plan.Actions, act)
	}

	sort.SliceStable(plan.Actions, func(i, j int) bool {
		ri, rj := opRank[plan.Actions[i].Op], opRank[plan.Actions[j].Op]
		if ri != rj {
			return ri < rj
		}
		// Within removals, work bottom-up so that positions stay stable
		// for anything still reading the rule base.
		return plan.Actions[i].Target.Position > plan.Actions[j].Target.Position
	})
	return plan
}

// pick chooses the first alternative on a finding that the operator
// allowed, applying PreferDisable on the way.
func pick(actions []Action, opt PlanOptions) (Action, bool) {
	for _, a := range actions {
		if opt.PreferDisable && a.Op == OpDelete {
			a.Op = OpDisable
			a.Behaviour = BehaviourPreserving
			a.Reason = a.Reason + " (disabled instead of deleted)"
		}
		if opt.Allow[a.Op] {
			return a, true
		}
	}
	return Action{}, false
}

// Empty reports whether there is nothing to do.
func (p *Plan) Empty() bool { return len(p.Actions) == 0 }

// ActionResult is what happened to one action.
type ActionResult struct {
	Action Action `json:"action"`
	Status string `json:"status"` // applied | failed | dry-run
	Error  string `json:"error,omitempty"`
}

// Apply executes the plan. With dryRun set, nothing is sent and every
// action comes back as "dry-run".
//
// Execution stops at the first failure: the plan is ordered, and later
// actions can assume the earlier ones landed.
func (p *Plan) Apply(ctx context.Context, c *Client, dryRun bool) ([]ActionResult, error) {
	results := make([]ActionResult, 0, len(p.Actions))
	for _, act := range p.Actions {
		if dryRun {
			results = append(results, ActionResult{Action: act, Status: "dry-run"})
			continue
		}
		if err := p.execute(ctx, c, act); err != nil {
			results = append(results, ActionResult{Action: act, Status: "failed", Error: err.Error()})
			return results, fmt.Errorf("applying %s: %w", act.Describe(), err)
		}
		results = append(results, ActionResult{Action: act, Status: "applied"})
	}
	return results, nil
}

func (p *Plan) execute(ctx context.Context, c *Client, act Action) error {
	target := act.TargetRef
	if target == "" {
		target = act.Target.Name
	}

	switch act.Op {
	case OpEnableLogging:
		_, err := c.UpdateIPv4Rule(ctx, target, map[string]any{"logTraffic": true})
		return err

	case OpDisable:
		_, err := c.UpdateIPv4Rule(ctx, target, map[string]any{"enabled": false})
		return err

	case OpMove:
		// The move endpoint identifies both rules by name only.
		return c.MoveIPv4Rule(ctx, act.Target.Name, act.Position, act.Anchor)

	case OpDelete:
		return c.DeleteIPv4Rule(ctx, target)

	case OpMerge:
		patch, err := p.mergePatch(act)
		if err != nil {
			return err
		}
		anchor := act.AnchorRef
		if anchor == "" {
			anchor = act.Anchor
		}
		if _, err := c.UpdateIPv4Rule(ctx, anchor, patch); err != nil {
			return fmt.Errorf("widening %q: %w", act.Anchor, err)
		}
		if err := c.DeleteIPv4Rule(ctx, target); err != nil {
			return fmt.Errorf("widened %q but could not delete %s: %w", act.Anchor, act.Target, err)
		}
		return nil
	}
	return fmt.Errorf("sophos: unsupported operation %q", act.Op)
}

// mergePatch builds the PATCH body that widens the surviving rule to cover
// both rules of a mergeable pair.
func (p *Plan) mergePatch(act Action) (map[string]any, error) {
	if act.intoIndex < 0 || act.intoIndex >= len(p.rules) ||
		act.fromIndex < 0 || act.fromIndex >= len(p.rules) {
		return nil, fmt.Errorf("sophos: merge references a rule outside the analyzed base")
	}
	a, b := &p.rules[act.intoIndex], &p.rules[act.fromIndex]

	switch act.dim {
	case dimSourceZones:
		return map[string]any{"sourceZones": unionZones(a.SourceZones, b.SourceZones)}, nil
	case dimDestinationZones:
		return map[string]any{"destinationZones": unionZones(a.DestinationZones, b.DestinationZones)}, nil
	case dimSourceNetworks:
		return map[string]any{"sourceNetworks": unionNetworks(a.SourceNetworks, b.SourceNetworks)}, nil
	case dimDestinationNetworks:
		return map[string]any{"destinationNetworks": unionNetworks(a.DestinationNetworks, b.DestinationNetworks)}, nil
	case dimServices:
		return map[string]any{"servicesOrGroups": unionServices(a.ServicesOrGroups, b.ServicesOrGroups)}, nil
	}
	return nil, fmt.Errorf("sophos: merge on unsupported dimension %v", act.dim)
}

func unionZones(a, b *ZoneSet) *ZoneSet {
	if a == nil || b == nil {
		return nil
	}
	if a.Any || b.Any {
		return &ZoneSet{Any: true}
	}
	return &ZoneSet{Zones: unionRefs(a.Zones, b.Zones)}
}

func unionNetworks(a, b *NetworkSet) *NetworkSet {
	if a == nil || b == nil {
		return nil
	}
	if a.Any || b.Any {
		return &NetworkSet{Any: true}
	}
	return &NetworkSet{
		Countries:     unionRefs(a.Countries, b.Countries),
		CountryGroups: unionRefs(a.CountryGroups, b.CountryGroups),
		FQDNAddresses: unionRefs(a.FQDNAddresses, b.FQDNAddresses),
		FQDNGroups:    unionRefs(a.FQDNGroups, b.FQDNGroups),
		IPv4Addresses: unionRefs(a.IPv4Addresses, b.IPv4Addresses),
		IPv4Groups:    unionRefs(a.IPv4Groups, b.IPv4Groups),
		MACAddresses:  unionRefs(a.MACAddresses, b.MACAddresses),
	}
}

func unionServices(a, b *ServiceSet) *ServiceSet {
	if a == nil || b == nil {
		return nil
	}
	if a.Any || b.Any {
		return &ServiceSet{Any: true}
	}
	return &ServiceSet{
		Services:      unionRefs(a.Services, b.Services),
		ServiceGroups: unionRefs(a.ServiceGroups, b.ServiceGroups),
	}
}

func unionRefs(a, b []NamedRef) []NamedRef {
	seen := map[string]struct{}{}
	var out []NamedRef
	for _, list := range [][]NamedRef{a, b} {
		for _, r := range list {
			name := strings.TrimSpace(r.Name)
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, NamedRef{Name: name})
		}
	}
	sortRefs(out)
	return out
}

// SaveRules writes the rule base to path as JSON. Run it before applying
// anything: the API has no undo, and this file is what a restore reads.
func SaveRules(path string, rules []Rule) error {
	buf, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return fmt.Errorf("sophos: encoding rules: %w", err)
	}
	if err := os.WriteFile(path, append(buf, '\n'), 0o600); err != nil {
		return fmt.Errorf("sophos: writing %s: %w", path, err)
	}
	return nil
}

// LoadRules reads a rule base previously written by SaveRules, or any JSON
// holding either a bare array of rules or the API's list envelope.
func LoadRules(path string) ([]Rule, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sophos: reading %s: %w", path, err)
	}
	var rules []Rule
	if err := json.Unmarshal(buf, &rules); err == nil {
		return rules, nil
	}
	var list RuleList
	if err := json.Unmarshal(buf, &list); err != nil {
		return nil, fmt.Errorf("sophos: %s is neither a rule array nor a rule list: %w", path, err)
	}
	if list.Items == nil {
		// The document parsed as an envelope only because JSON ignores the
		// fields it does not know. Nothing in it is a rule.
		return nil, fmt.Errorf("sophos: %s holds no rules", path)
	}
	return list.Items, nil
}
