// Command impactcmd shares validation planning between local checks and CI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/testkit/impact"
)

func main() {
	base := flag.String("base", "", "comparison base (default origin/main merge-base)")
	head := flag.String("head", "HEAD", "checked-out candidate")
	worktree := flag.Bool("worktree", false, "include local tracked and untracked edits")
	full := flag.Bool("full", false, "select complete main/release validation")
	output := flag.String("output", "", "write a temporary JSON plan")
	input := flag.String("plan", "", "read and validate a saved plan")
	run := flag.String("run", "", "execute a shared plan action: quality")
	results := flag.String("verify-results", "", "verify Actions needs JSON against the plan")
	github := flag.Bool("github-output", false, "append job selections to GITHUB_OUTPUT")
	goTool := flag.String("go", "go", "Go executable for dependency discovery")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := execute(ctx, *base, *head, *worktree, *full, *output, *input, *run, *results, *github, *goTool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, base, head string, worktree, full bool, output, input, run, results string, github bool, goTool string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	var p impact.Plan
	if input != "" {
		p, err = impact.Read(ctx, root, input)
	} else {
		p, err = impact.Load(ctx, root, impact.Options{Base: base, Head: head, Worktree: worktree, Full: full, Go: goTool})
	}
	if err != nil {
		return err
	}
	if output != "" {
		if err := impact.Write(output, p); err != nil {
			return err
		}
	}
	if run != "" {
		if run != "quality" {
			return fmt.Errorf("unknown plan action %q", run)
		}
		return impact.RunQuality(ctx, root, p)
	}
	if results != "" {
		raw, err := os.ReadFile(results)
		if err != nil {
			return err
		}
		var needs map[string]struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal(raw, &needs); err != nil {
			return err
		}
		jobs := map[string]string{}
		for name, result := range needs {
			jobs[name] = result.Result
		}
		if err := p.VerifyResults(jobs); err != nil {
			return err
		}
		fmt.Println("all required planned validations succeeded; planned exclusions remain explicit")
		return nil
	}
	if github {
		path := os.Getenv("GITHUB_OUTPUT")
		if path == "" {
			return fmt.Errorf("GITHUB_OUTPUT is absent")
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		for job, required := range p.Required() {
			if _, err = fmt.Fprintf(file, "%s=%t\n", strings.ReplaceAll(job, "-", "_"), required); err != nil {
				return err
			}
		}
		// A skipped matrix job still needs syntactically valid configuration.
		groups := slices.Clone(p.Groups)
		if len(groups) == 0 {
			groups = []string{"other"}
		}
		encoded, err := json.Marshal(groups)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(file, "groups=%s\n", encoded); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}
