package sophos

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// kindOrder fixes the order the summary table lists the checks in, worst
// first, so the top of the table is where an operator should start.
var kindOrder = []Kind{
	KindShadowed,
	KindPermissive,
	KindRedundant,
	KindDuplicate,
	KindMergeable,
	KindBroadService,
	KindNoInspection,
	KindNoLogging,
	KindDisabled,
}

var kindHeadline = map[Kind]string{
	KindShadowed:     "shadowed rules (never evaluate, policy not in force)",
	KindPermissive:   "any-to-any accept rules",
	KindRedundant:    "redundant rules (already covered above)",
	KindDuplicate:    "duplicate rules",
	KindMergeable:    "adjacent rules that could be one",
	KindBroadService: "accept rules with no service restriction",
	KindNoInspection: "accept rules with no security profile",
	KindNoLogging:    "rules that do not log",
	KindDisabled:     "disabled rules still in the base",
}

// WriteText renders a human-readable report, optionally followed by the
// plan and by what came of applying it. plan and results may be nil.
func (r *Report) WriteText(w io.Writer, plan *Plan, results []ActionResult) error {
	b := &strings.Builder{}

	fmt.Fprintf(b, "Sophos firewall policy review — %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(b, "Source: %s\n", orDash(r.Source))
	fmt.Fprintf(b, "Rules:  %d total, %d enabled\n\n", r.Rules, r.Enabled)

	if len(r.Findings) == 0 {
		fmt.Fprintln(b, "No findings. The rule base is clean against every check this tool runs.")
		_, err := io.WriteString(w, b.String())
		return err
	}

	// Summary table.
	fmt.Fprintln(b, "Summary")
	fmt.Fprintln(b, strings.Repeat("─", 72))
	for _, k := range kindOrder {
		n := r.Count(k)
		if n == 0 {
			continue
		}
		fmt.Fprintf(b, "  %4d  %s\n", n, kindHeadline[k])
	}
	fmt.Fprintf(b, "  %4d  findings in total\n\n", len(r.Findings))

	// Findings, grouped by severity.
	current := Severity(-1)
	for _, f := range r.Findings {
		if f.Severity != current {
			current = f.Severity
			fmt.Fprintf(b, "\n%s\n%s\n", strings.ToUpper(current.String()), strings.Repeat("─", 72))
		}
		fmt.Fprintf(b, "\n[%s] %s\n", f.Kind, f.Title)
		for _, line := range wrap(f.Detail, 70) {
			fmt.Fprintf(b, "    %s\n", line)
		}
		for _, a := range f.Actions {
			fmt.Fprintf(b, "    → %-15s %s  (%s)\n", "-allow "+string(a.Op), a.Reason, a.Behaviour)
		}
	}

	if plan != nil {
		b.WriteString("\n\n")
		writePlan(b, plan)
	}
	if len(results) > 0 {
		writeResults(b, results)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// WriteText prints just the plan. The fwopt command shows this on stderr
// before asking for confirmation, so that the plan can be reviewed even
// when the report itself is JSON on stdout.
func (p *Plan) WriteText(w io.Writer) error {
	b := &strings.Builder{}
	writePlan(b, p)
	_, err := io.WriteString(w, b.String())
	return err
}

func writePlan(b *strings.Builder, plan *Plan) {
	fmt.Fprintf(b, "Plan\n%s\n", strings.Repeat("─", 72))
	if plan.Empty() {
		fmt.Fprintln(b, "Nothing to do with the operations currently allowed.")
	} else {
		for i, a := range plan.Actions {
			fmt.Fprintf(b, "%3d. %s\n", i+1, a.Describe())
			fmt.Fprintf(b, "     %s — %s\n", a.Reason, a.Behaviour)
		}
	}
	if len(plan.Skipped) == 0 {
		return
	}
	ops := make([]Op, 0, len(plan.Skipped))
	for op := range plan.Skipped {
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return opRank[ops[i]] < opRank[ops[j]] })

	fmt.Fprintln(b, "\nNot in the plan — add these to -allow to include them:")
	for _, op := range ops {
		fmt.Fprintf(b, "  -allow %-8s %d finding(s)\n", op, plan.Skipped[op])
	}
}

// jsonReport is the shape of -format json. It is deliberately separate
// from Report so that the wire format does not drift every time an
// internal field moves.
type jsonReport struct {
	GeneratedAt string         `json:"generatedAt"`
	Source      string         `json:"source"`
	Rules       int            `json:"rules"`
	Enabled     int            `json:"enabledRules"`
	Summary     map[string]int `json:"summary"`
	Findings    []Finding      `json:"findings"`
	Plan        []Action       `json:"plan,omitempty"`
	Results     []ActionResult `json:"results,omitempty"`
}

// WriteJSON renders the report, and optionally the plan and what came of
// it, as a single JSON document.
func (r *Report) WriteJSON(w io.Writer, plan *Plan, results []ActionResult) error {
	doc := jsonReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Source:      r.Source,
		Rules:       r.Rules,
		Enabled:     r.Enabled,
		Summary:     map[string]int{},
		Findings:    r.Findings,
		Results:     results,
	}
	for _, k := range kindOrder {
		if n := r.Count(k); n > 0 {
			doc.Summary[string(k)] = n
		}
	}
	if plan != nil {
		doc.Plan = plan.Actions
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// writeResults prints what happened to each action of an applied plan.
func writeResults(b *strings.Builder, results []ActionResult) {
	fmt.Fprintf(b, "\nResults\n%s\n", strings.Repeat("─", 72))
	for _, res := range results {
		mark := "✓"
		switch res.Status {
		case "failed":
			mark = "✗"
		case "dry-run":
			mark = "·"
		}
		fmt.Fprintf(b, " %s %-8s %s\n", mark, res.Status, res.Action.Describe())
		if res.Error != "" {
			fmt.Fprintf(b, "     %s\n", res.Error)
		}
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// wrap breaks text into lines of at most width runes, on word boundaries.
func wrap(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, w := range words[1:] {
		if len([]rune(line))+1+len([]rune(w)) > width {
			lines = append(lines, line)
			line = w
			continue
		}
		line += " " + w
	}
	return append(lines, line)
}
