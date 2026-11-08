package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"claude-orchestrator/internal/plugins/compile"
)

// toolchain files a plugin folder may hold for the author's editor; none of them runs in lucode
var toolchain = []string{"node_modules", "test", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "tsconfig.json", "jsconfig.json"}

// skipped is what stays out of the zip besides names starting with . or _: the toolchain's files
// and the sources a build compiles.
func skipped(dir string) map[string]bool {
	skip := map[string]bool{}
	for _, name := range toolchain {
		skip[name] = true
	}
	if m, err := compile.ReadManifest(dir); err == nil {
		for _, src := range compile.Sources(m) {
			skip[src] = true
		}
	}
	return skip
}

func runBuild(dir string, out io.Writer) error {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	res, err := compile.Build(dir)
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(out, "warning:", w)
	}
	for _, o := range res.Outputs {
		fmt.Fprintf(out, "built %-22s %s -> %s (%s)\n", o.What, o.Source, o.File, size(o.Bytes))
	}
	return nil
}

func size(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

func cmdBuild(args []string, out io.Writer) error {
	fset := flag.NewFlagSet("build", flag.ContinueOnError)
	dir, err := parse(fset, args)
	if err != nil {
		return err
	}
	m, err := compile.ReadManifest(dir)
	if err != nil {
		return err
	}
	if m.Build == nil {
		fmt.Fprintln(out, `nothing to build: plugin.json has no "build"`)
		return nil
	}
	return runBuild(dir, out)
}

func cmdTest(args []string, out io.Writer) error {
	fset := flag.NewFlagSet("test", flag.ContinueOnError)
	filter := fset.String("run", "", "only the tests whose name contains this")
	noBuild := fset.Bool("no-build", false, "run the tests on what is built already")
	dir, err := parse(fset, args)
	if err != nil {
		return err
	}
	if !*noBuild {
		if err := runBuild(dir, out); err != nil {
			return err
		}
	}
	sum, err := compile.RunTests(dir, *filter, out)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%d passed, %d failed", sum.Passed, sum.Failed)
	if sum.Skipped > 0 {
		fmt.Fprintf(out, ", %d skipped", sum.Skipped)
	}
	fmt.Fprintln(out)
	if sum.Failed > 0 {
		return errors.New("some tests failed")
	}
	if sum.Passed == 0 && *filter != "" {
		return fmt.Errorf("no test's name contains %q", *filter)
	}
	return nil
}
