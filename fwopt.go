// fwopt.go — the "fwopt" subcommand: firewall policy optimization for
// Sophos Firewall.
//
// It reads the IPv4 rule base over the Firewall Configuration REST API,
// reports what is dead, duplicated, over-broad or unlogged, and — only
// when explicitly told to — applies the fixes back through the API.
//
// The engine lives in the sophos package; this file is argument parsing,
// safety rails and output plumbing.

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"ztna-lab/sophos"
)

// Exit codes. 3 is reserved for "the run worked, the policy did not pass",
// so that a pipeline can tell a broken tool from a failing rule base.
const (
	exitOK       = 0
	exitError    = 1
	exitFindings = 3
)

func runFwopt(args []string) {
	fs := flag.NewFlagSet("fwopt", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		host     = fs.String("host", os.Getenv("SOPHOS_FW_HOST"), "firewall address and admin port, e.g. 10.0.0.1:4444 (env SOPHOS_FW_HOST)")
		apiKey   = fs.String("api-key", os.Getenv("SOPHOS_FW_API_KEY"), "API key sent as a bearer token (env SOPHOS_FW_API_KEY)")
		insecure = fs.Bool("insecure", os.Getenv("SOPHOS_FW_INSECURE") == "true", "skip TLS verification — needed for the default self-signed WebAdmin certificate (env SOPHOS_FW_INSECURE)")
		timeout  = fs.Duration("timeout", 30*time.Second, "per-request timeout")

		in   = fs.String("in", "", "analyze a saved rule dump instead of calling the firewall")
		save = fs.String("save", "", "write the fetched rules to this file")

		format      = fs.String("format", "text", "report format: text or json")
		out         = fs.String("out", "", "write the report here instead of stdout")
		minSeverity = fs.String("min-severity", "info", "drop findings below this level: info, low, medium, high")
		skip        = fs.String("skip", "", "comma-separated checks to leave out (e.g. no-logging,disabled)")
		failOn      = fs.String("fail-on", "", "exit 3 if any finding reaches this level: low, medium, high")

		apply         = fs.Bool("apply", false, "send the plan to the firewall (without this, nothing is written)")
		allow         = fs.String("allow", string(sophos.OpEnableLogging), "operations the plan may use: "+opList()+", or all/none")
		preferDisable = fs.Bool("prefer-disable", false, "disable rules instead of deleting them, so the cleanup is reversible")
		backup        = fs.String("backup", "", "where to write the pre-change rule backup (default: ./sophos-rules-<timestamp>.json)")
		assumeYes     = fs.Bool("yes", false, "do not ask for confirmation before applying")
	)

	fs.Usage = func() {
		fmt.Fprint(os.Stderr, fwoptUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(exitError)
	}

	if err := fwopt(fwoptConfig{
		host: *host, apiKey: *apiKey, insecure: *insecure, timeout: *timeout,
		in: *in, save: *save,
		format: *format, out: *out, minSeverity: *minSeverity, skip: *skip, failOn: *failOn,
		apply: *apply, allow: *allow, preferDisable: *preferDisable,
		backup: *backup, assumeYes: *assumeYes,
	}); err != nil {
		var fe *findingsError
		if errors.As(err, &fe) {
			fmt.Fprintf(os.Stderr, "\n%s\n", fe.Error())
			os.Exit(exitFindings)
		}
		fmt.Fprintf(os.Stderr, "fwopt: %v\n", err)
		os.Exit(exitError)
	}
	os.Exit(exitOK)
}

type fwoptConfig struct {
	host, apiKey string
	insecure     bool
	timeout      time.Duration

	in, save string

	format, out, minSeverity, skip, failOn string

	apply         bool
	allow         string
	preferDisable bool
	backup        string
	assumeYes     bool
}

// findingsError signals a clean run whose findings breached -fail-on. It
// is what separates exit 3 ("the rule base did not pass") from exit 1
// ("the tool could not do its job").
type findingsError struct{ msg string }

func (e *findingsError) Error() string { return e.msg }

func fwopt(cfg fwoptConfig) error {
	minSev, err := sophos.ParseSeverity(cfg.minSeverity)
	if err != nil {
		return err
	}
	allow, err := sophos.ParseOps(cfg.allow)
	if err != nil {
		return err
	}
	switch cfg.format {
	case "text", "json":
	default:
		return fmt.Errorf("unknown format %q (text or json)", cfg.format)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// ---- load the rule base ----

	var (
		client *sophos.Client
		rules  []sophos.Rule
		source string
	)
	switch {
	case cfg.in != "":
		if cfg.apply {
			return fmt.Errorf("-apply needs a live firewall; -in analyzes a file that may no longer match it")
		}
		if rules, err = sophos.LoadRules(cfg.in); err != nil {
			return err
		}
		source = cfg.in
	default:
		if client, err = sophos.NewClient(sophos.Config{
			Host: cfg.host, APIKey: cfg.apiKey, Insecure: cfg.insecure, Timeout: cfg.timeout,
		}); err != nil {
			return err
		}
		if rules, err = client.ListIPv4Rules(ctx); err != nil {
			return err
		}
		source = client.BaseURL()
	}
	if len(rules) == 0 {
		return fmt.Errorf("no IPv4 rules came back from %s", source)
	}
	if cfg.save != "" {
		if err := sophos.SaveRules(cfg.save, rules); err != nil {
			return err
		}
	}

	// ---- analyze ----

	rep := sophos.Analyze(rules, sophos.Options{
		MinSeverity: minSev,
		Skip:        parseKinds(cfg.skip),
		Source:      source,
	})
	plan := sophos.BuildPlan(rep, sophos.PlanOptions{Allow: allow, PreferDisable: cfg.preferDisable})

	w, closeOut, err := openOutput(cfg.out)
	if err != nil {
		return err
	}
	defer closeOut()

	// ---- apply ----
	//
	// Everything interactive goes to stderr, and the report is written
	// once, at the end, so that stdout carries exactly one document even
	// when it is JSON being piped somewhere.

	var (
		results  []sophos.ActionResult
		applyErr error
	)
	if cfg.apply {
		switch {
		case plan.Empty():
			fmt.Fprintln(os.Stderr, "fwopt: nothing to apply with the operations currently allowed (see -allow).")
		default:
			fmt.Fprintln(os.Stderr)
			if err := plan.WriteText(os.Stderr); err != nil {
				return err
			}
			if err := confirm(plan, cfg.assumeYes); err != nil {
				return err
			}
			backupPath, err := writeBackup(cfg.backup, rules)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Backup of %d rules written to %s\n", len(rules), backupPath)

			results, applyErr = plan.Apply(ctx, client, false)
			if applyErr != nil {
				// Report what did land before bailing out: the firewall is
				// now in a state nobody planned for.
				_ = writeReport(w, rep, plan, cfg.format, results)
				closeOut()
				return fmt.Errorf("%w\nThe firewall is partly changed. Restore from %s if needed", applyErr, backupPath)
			}
		}
	}

	if err := writeReport(w, rep, plan, cfg.format, results); err != nil {
		return err
	}
	return failOnCheck(rep, cfg.failOn)
}

func writeReport(w io.Writer, rep *sophos.Report, plan *sophos.Plan, format string, results []sophos.ActionResult) error {
	if format == "json" {
		return rep.WriteJSON(w, plan, results)
	}
	return rep.WriteText(w, plan, results)
}

// confirm asks before anything is written to the firewall. Anything other
// than a typed "yes" aborts, including end-of-input: an unattended run has
// to say so with -yes rather than have silence read as consent.
func confirm(plan *sophos.Plan, assumeYes bool) error {
	if assumeYes {
		return nil
	}
	changing := 0
	for _, a := range plan.Actions {
		if a.Behaviour == sophos.BehaviourChanging {
			changing++
		}
	}
	fmt.Fprintf(os.Stderr, "\nAbout to apply %d action(s) to the firewall", len(plan.Actions))
	if changing > 0 {
		fmt.Fprintf(os.Stderr, ", %d of which change what it forwards", changing)
	}
	fmt.Fprint(os.Stderr, ".\nType 'yes' to continue: ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return fmt.Errorf("aborted: nothing to read from stdin — pass -yes to apply unattended")
	}
	if strings.TrimSpace(strings.ToLower(line)) != "yes" {
		return fmt.Errorf("aborted")
	}
	return nil
}

func writeBackup(path string, rules []sophos.Rule) (string, error) {
	if path == "" {
		path = fmt.Sprintf("sophos-rules-%s.json", time.Now().UTC().Format("20060102-150405"))
	}
	if err := sophos.SaveRules(path, rules); err != nil {
		return "", fmt.Errorf("refusing to apply without a backup: %w", err)
	}
	return path, nil
}

// failOnCheck turns findings into a non-zero exit for pipeline use.
func failOnCheck(rep *sophos.Report, level string) error {
	if strings.TrimSpace(level) == "" {
		return nil
	}
	threshold, err := sophos.ParseSeverity(level)
	if err != nil {
		return err
	}
	n := 0
	for _, f := range rep.Findings {
		if f.Severity >= threshold {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return &findingsError{msg: fmt.Sprintf("fwopt: %d finding(s) at %s or above (-fail-on %s)", n, threshold, level)}
}

func openOutput(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", path, err)
	}
	var closed bool
	return f, func() {
		if !closed {
			closed = true
			_ = f.Close()
		}
	}, nil
}

func parseKinds(s string) []sophos.Kind {
	var kinds []sophos.Kind
	for _, part := range strings.Split(s, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			kinds = append(kinds, sophos.Kind(part))
		}
	}
	return kinds
}

func opList() string {
	names := make([]string, len(sophos.AllOps))
	for i, op := range sophos.AllOps {
		names[i] = string(op)
	}
	return strings.Join(names, ", ")
}

const fwoptUsage = `ztna-lab fwopt — Sophos Firewall policy optimization

Reads the IPv4 rule base over the Firewall Configuration API, reports dead,
duplicated, over-broad and unlogged rules, and applies the fixes on request.
Read-only unless -apply is given.

  # review a firewall
  ztna-lab fwopt -host 10.0.0.1:4444 -api-key "$KEY" -insecure

  # keep a copy, then work offline against it
  ztna-lab fwopt -host 10.0.0.1:4444 -api-key "$KEY" -save rules.json
  ztna-lab fwopt -in rules.json -format json -out review.json

  # apply only the reversible fixes
  ztna-lab fwopt -host 10.0.0.1:4444 -api-key "$KEY" -apply -allow log,merge

  # clean out dead rules by disabling rather than deleting them
  ztna-lab fwopt -host 10.0.0.1:4444 -api-key "$KEY" -apply \
      -allow disable -prefer-disable

Checks: shadowed, redundant, duplicate, mergeable, permissive,
broad-service, no-inspection, no-logging, disabled.

Flags:
`
