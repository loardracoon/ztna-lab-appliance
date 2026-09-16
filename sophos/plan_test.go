package sophos

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func allowOps(ops ...Op) map[Op]bool {
	m := map[Op]bool{}
	for _, op := range ops {
		m[op] = true
	}
	return m
}

func planFor(t *testing.T, rules []Rule, opt PlanOptions) *Plan {
	t.Helper()
	return BuildPlan(Analyze(rules, Options{}), opt)
}

func opsIn(plan *Plan) []Op {
	out := make([]Op, 0, len(plan.Actions))
	for _, a := range plan.Actions {
		out = append(out, a.Op)
	}
	return out
}

func TestParseOps(t *testing.T) {
	got, err := ParseOps("log, delete")
	if err != nil {
		t.Fatalf("ParseOps: %v", err)
	}
	if !reflect.DeepEqual(got, allowOps(OpEnableLogging, OpDelete)) {
		t.Errorf("ParseOps(log, delete) = %v", got)
	}

	all, err := ParseOps("all")
	if err != nil || len(all) != len(AllOps) {
		t.Errorf("ParseOps(all) = %v, %v", all, err)
	}
	if none, err := ParseOps("none"); err != nil || len(none) != 0 {
		t.Errorf("ParseOps(none) = %v, %v", none, err)
	}
	if empty, err := ParseOps(""); err != nil || len(empty) != 0 {
		t.Errorf("ParseOps(empty) = %v, %v", empty, err)
	}
	if _, err := ParseOps("rewrite"); err == nil {
		t.Error("ParseOps should reject an unknown operation")
	}
}

func TestPlanHonoursTheAllowList(t *testing.T) {
	rules := []Rule{
		rule("accept-all", ActionAccept, anyServices, noLog),
		rule("block-telnet", ActionDrop, services("telnet")),
	}

	// With nothing allowed the plan is empty but still says what it held back.
	empty := planFor(t, rules, PlanOptions{Allow: allowOps()})
	if !empty.Empty() {
		t.Fatalf("plan should be empty, got %v", opsIn(empty))
	}
	if empty.Skipped[OpMove] == 0 || empty.Skipped[OpEnableLogging] == 0 {
		t.Errorf("skipped should record every remedy it could not use: %v", empty.Skipped)
	}

	// Allowing only logging picks up the log fix and nothing else.
	logs := planFor(t, rules, PlanOptions{Allow: allowOps(OpEnableLogging)})
	if got := opsIn(logs); !reflect.DeepEqual(got, []Op{OpEnableLogging}) {
		t.Errorf("ops = %v, want just the log fix", got)
	}
}

func TestPlanPicksOneAlternativePerFinding(t *testing.T) {
	// The actions on a finding are alternatives. pick takes the first one
	// the operator allowed and never returns two.
	alternatives := []Action{
		{Op: OpMove, Target: RuleRef{Name: "r"}},
		{Op: OpDelete, Target: RuleRef{Name: "r"}},
	}

	got, ok := pick(alternatives, PlanOptions{Allow: allowOps(OpMove, OpDelete)})
	if !ok || got.Op != OpMove {
		t.Errorf("pick = %v/%v, want the first allowed alternative", got.Op, ok)
	}
	got, ok = pick(alternatives, PlanOptions{Allow: allowOps(OpDelete)})
	if !ok || got.Op != OpDelete {
		t.Errorf("pick = %v/%v, want the allowed fallback", got.Op, ok)
	}
	if _, ok := pick(alternatives, PlanOptions{Allow: allowOps(OpEnableLogging)}); ok {
		t.Error("pick should decline when no alternative is allowed")
	}
}

func TestShadowedRuleIsNeverDeletedAutomatically(t *testing.T) {
	// A shadowed rule does something the rule above it does not. Removing
	// it on -allow delete would quietly erase a policy the admin wrote,
	// along with the evidence that it is not in force.
	rules := []Rule{
		rule("accept-all", ActionAccept, anyServices),
		rule("block-telnet", ActionDrop, services("telnet")),
	}

	del := planFor(t, rules, PlanOptions{Allow: allowOps(OpDelete)})
	if !del.Empty() {
		t.Errorf("-allow delete produced %v for a shadowed rule", opsIn(del))
	}

	// Promotion is offered, and only when explicitly allowed.
	move := planFor(t, rules, PlanOptions{Allow: allowOps(OpMove)})
	if got := opsIn(move); !reflect.DeepEqual(got, []Op{OpMove}) {
		t.Errorf("ops = %v, want a move", got)
	}
}

func TestPreferDisableRewritesDeletes(t *testing.T) {
	rules := []Rule{
		rule("allow-https", ActionAccept),
		rule("allow-https-copy", ActionAccept),
	}
	plan := planFor(t, rules, PlanOptions{Allow: allowOps(OpDisable), PreferDisable: true})

	if got := opsIn(plan); !reflect.DeepEqual(got, []Op{OpDisable}) {
		t.Fatalf("ops = %v, want a disable", got)
	}
	if plan.Actions[0].Behaviour != BehaviourPreserving {
		t.Errorf("behaviour = %s, want preserving", plan.Actions[0].Behaviour)
	}
	if !strings.Contains(plan.Actions[0].Reason, "disabled instead of deleted") {
		t.Errorf("reason should say the delete was downgraded, got %q", plan.Actions[0].Reason)
	}

	// The rewrite still has to clear the allow list.
	blocked := planFor(t, rules, PlanOptions{Allow: allowOps(OpDelete), PreferDisable: true})
	if !blocked.Empty() {
		t.Errorf("a disable must not ride in on -allow delete: %v", opsIn(blocked))
	}
}

func TestPlanNeverTargetsOneRuleTwice(t *testing.T) {
	rules := []Rule{
		rule("accept-all", ActionAccept, anyServices, noLog),
		rule("also-broad", ActionAccept, anyServices, noLog),
		rule("narrow", ActionAccept, services("https"), noLog),
	}
	plan := planFor(t, rules, PlanOptions{Allow: allowOps(OpEnableLogging, OpDelete, OpMove, OpMerge)})

	seen := map[string]Op{}
	for _, a := range plan.Actions {
		if prev, dup := seen[a.Target.Name]; dup {
			t.Fatalf("rule %q targeted twice: %s and %s", a.Target.Name, prev, a.Op)
		}
		seen[a.Target.Name] = a.Op
	}
}

func TestPlanDropsAMoveWhoseAnchorIsBeingRemoved(t *testing.T) {
	// "dup" is deleted as a duplicate of "accept-all"; "block-telnet"
	// would be promoted above "dup". Once "dup" is gone that move has no
	// anchor, so it must not be attempted.
	rules := []Rule{
		rule("accept-all", ActionAccept, anyServices, srcNets("hq")),
		rule("dup", ActionAccept, anyServices, srcNets("hq")),
		rule("block-telnet", ActionDrop, services("telnet"), srcNets("hq")),
	}
	rep := Analyze(rules, Options{})

	shadow := findingFor(rep, KindShadowed, "block-telnet")
	if shadow == nil {
		t.Fatalf("setup: expected block-telnet to be shadowed, got %s", summarize(rep))
	}
	// Point the move at the rule that is about to be deleted.
	shadow.Actions[0].Anchor = "dup"

	plan := BuildPlan(rep, PlanOptions{Allow: allowOps(OpDelete, OpMove)})
	for _, a := range plan.Actions {
		if a.Op == OpMove && a.Anchor == "dup" {
			t.Fatalf("plan kept a move anchored to a rule it deletes: %s", a.Describe())
		}
	}
}

func TestPlanOrdersLoggingBeforeRemovals(t *testing.T) {
	rules := []Rule{
		rule("quiet", ActionAccept, srcNets("hq"), services("https"), noLog),
		rule("broad", ActionAccept, srcNets("dc"), anyServices),
		rule("covered", ActionAccept, srcNets("dc"), services("https")),
	}
	plan := planFor(t, rules, PlanOptions{Allow: allowOps(OpEnableLogging, OpDelete)})

	ops := opsIn(plan)
	if len(ops) < 2 {
		t.Fatalf("expected at least a log fix and a delete, got %v", ops)
	}
	seenDelete := false
	for _, op := range ops {
		if op == OpDelete {
			seenDelete = true
		}
		if seenDelete && op == OpEnableLogging {
			t.Fatalf("a log fix runs after a delete in %v", ops)
		}
	}
}

func TestRemovalsRunBottomUp(t *testing.T) {
	rules := []Rule{
		rule("broad", ActionAccept, anyServices),
		rule("covered-a", ActionAccept, services("https")),
		rule("covered-b", ActionAccept, services("ssh")),
	}
	plan := planFor(t, rules, PlanOptions{Allow: allowOps(OpDelete)})
	if len(plan.Actions) != 2 {
		t.Fatalf("expected two deletes, got %v", opsIn(plan))
	}
	if plan.Actions[0].Target.Position < plan.Actions[1].Target.Position {
		t.Errorf("deletes should run bottom-up, got positions %d then %d",
			plan.Actions[0].Target.Position, plan.Actions[1].Target.Position)
	}
}

// ---------- merge execution ----------

func TestMergePatchUnionsTheDifferingDimension(t *testing.T) {
	rules := []Rule{
		rule("web-a", ActionAccept, noProfile, srcNets("branch-a", "branch-c"), services("https")),
		rule("web-b", ActionAccept, noProfile, srcNets("branch-b"), services("https")),
	}
	plan := planFor(t, rules, PlanOptions{Allow: allowOps(OpMerge)})
	if len(plan.Actions) != 1 || plan.Actions[0].Op != OpMerge {
		t.Fatalf("expected one merge, got %v", opsIn(plan))
	}

	patch, err := plan.mergePatch(plan.Actions[0])
	if err != nil {
		t.Fatalf("mergePatch: %v", err)
	}
	set, ok := patch["sourceNetworks"].(*NetworkSet)
	if !ok {
		t.Fatalf("patch = %#v, want a sourceNetworks replacement", patch)
	}
	var got []string
	for _, r := range set.IPv4Groups {
		got = append(got, r.Name)
	}
	want := []string{"branch-a", "branch-b", "branch-c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("union = %v, want %v", got, want)
	}
}

func TestMergePatchKeepsAnyWhenEitherSideIsAny(t *testing.T) {
	got := unionNetworks(&NetworkSet{IPv4Groups: refs("hq")}, &NetworkSet{Any: true})
	if !got.Any || len(got.IPv4Groups) != 0 {
		t.Errorf("union with any = %#v, want {Any:true}", got)
	}
}

func TestMergeWidensThenDeletes(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method {
		case http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "branch-b") {
				t.Errorf("PATCH body did not carry the union: %s", body)
			}
			_, _ = w.Write([]byte(`{"id":"id-web-a"}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"deleted":true}`))
		}
	}))
	defer srv.Close()

	rules := []Rule{
		rule("web-a", ActionAccept, noProfile, srcNets("branch-a"), services("https")),
		rule("web-b", ActionAccept, noProfile, srcNets("branch-b"), services("https")),
	}
	plan := planFor(t, rules, PlanOptions{Allow: allowOps(OpMerge)})

	c := testClient(t, srv.URL)
	if _, err := plan.Apply(context.Background(), c, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []string{
		"PATCH /api/firewall-config/v1/firewall/rules/ipv4/id-web-a",
		"DELETE /api/firewall-config/v1/firewall/rules/ipv4/id-web-b",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

// ---------- apply ----------

func TestDryRunSendsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("dry run reached the firewall: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	rules := []Rule{rule("quiet", ActionAccept, noLog)}
	plan := planFor(t, rules, PlanOptions{Allow: allowOps(OpEnableLogging)})

	results, err := plan.Apply(context.Background(), testClient(t, srv.URL), true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(results) != 1 || results[0].Status != "dry-run" {
		t.Errorf("results = %+v, want a single dry-run entry", results)
	}
}

func TestApplyStopsAtTheFirstFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden","message":"read-only API key"}`))
	}))
	defer srv.Close()

	rules := []Rule{
		rule("quiet-a", ActionAccept, srcNets("hq"), services("https"), noLog),
		rule("quiet-b", ActionAccept, srcNets("dc"), services("ssh"), noLog),
	}
	plan := planFor(t, rules, PlanOptions{Allow: allowOps(OpEnableLogging)})
	if len(plan.Actions) != 2 {
		t.Fatalf("setup: expected two log fixes, got %v", opsIn(plan))
	}

	results, err := plan.Apply(context.Background(), testClient(t, srv.URL), false)
	if err == nil {
		t.Fatal("Apply should have failed")
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1 — execution must stop at the first failure", calls)
	}
	if len(results) != 1 || results[0].Status != "failed" {
		t.Errorf("results = %+v, want one failed entry", results)
	}
	if !strings.Contains(err.Error(), "read-only API key") {
		t.Errorf("error should carry the firewall's message, got %v", err)
	}
}

// ---------- persistence ----------

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	rules := []Rule{rule("a", ActionAccept), rule("b", ActionDrop, services("telnet"))}

	if err := SaveRules(path, rules); err != nil {
		t.Fatalf("SaveRules: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// The dump is a copy of the firewall's policy; it should not be
	// world-readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup mode = %o, want 600", perm)
	}

	got, err := LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(got) != 2 || got[0].Name != "a" || got[1].Action != ActionDrop {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestLoadRulesAcceptsTheAPIEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "list.json")
	body, _ := json.Marshal(RuleList{Items: []Rule{rule("a", ActionAccept)}, Pages: Pages{Current: 1}})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("got %+v, want the envelope's items", got)
	}
}

func TestLoadRulesRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"unexpected": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRules(path); err == nil {
		t.Error("LoadRules should reject a document that is not a rule base")
	}
}
