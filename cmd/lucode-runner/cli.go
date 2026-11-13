package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"claude-orchestrator/internal/plugins"
	"claude-orchestrator/internal/runnercfg"
)

// The runner's own command line: sign in once, say where agents work, start. The settings live in
// ~/.lucode-runner/config.json (internal/runnercfg); the flags of the plain `lucode-runner --nats ...`
// still work for a runner that is run from a service file.

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "lucode-runner: "+format+"\n", a...)
	os.Exit(1)
}

func loadConfig() (*runnercfg.Config, string) {
	path := runnercfg.DefaultPath()
	cfg, err := runnercfg.Load(path)
	if err != nil {
		die("%v", err)
	}
	return cfg, path
}

var stdin = bufio.NewReader(os.Stdin)

// ask prints a question with its default in brackets and returns the answer, or the default.
func ask(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := stdin.ReadString('\n')
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

// askPassword reads a password without echoing it when stdin is a terminal.
func askPassword(label string) string {
	fmt.Printf("%s: ", label)
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println()
		if err != nil {
			die("cannot read the password: %v", err)
		}
		return string(b)
	}
	line, _ := stdin.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

const authUsage = `usage:
  lucode-runner auth login [--hub lucode.home] [--user NAME] [--profile NAME] [--ca-file ca.pem]
  lucode-runner auth logout [--profile NAME]
  lucode-runner auth status [--profile NAME]`

func runAuth(args []string) {
	if len(args) == 0 {
		die("%s", authUsage)
	}
	switch args[0] {
	case "login":
		runLogin(args[1:])
	case "logout":
		runLogout(args[1:])
	case "status":
		runStatus(args[1:])
	default:
		die("%s", authUsage)
	}
}

func runLogin(args []string) {
	fs := flag.NewFlagSet("auth login", flag.ExitOnError)
	hub := fs.String("hub", "", "the hub: lucode.home, https://lucode.home or host:port for plain http")
	user := fs.String("user", "", "user name on the hub")
	profile := fs.String("profile", "", "profile to save it in (default: the current one)")
	ca := fs.String("ca-file", "", "CA that signed the hub's certificate, if this machine does not trust it")
	pwStdin := fs.Bool("password-stdin", false, "read the password from standard input (for scripts)")
	_ = fs.Parse(args)

	cfg, path := loadConfig()
	name := cfg.Name(*profile)
	old := cfg.Profiles[name]
	if old == nil {
		old = &runnercfg.Profile{}
	}

	h := *hub
	if h == "" {
		def := old.Hub
		if def == "" {
			def = "lucode.home"
		}
		h = ask("Hub", def)
	}
	h, err := runnercfg.NormalizeHub(h)
	if err != nil {
		die("%v", err)
	}
	u := *user
	if u == "" {
		u = ask("User", old.User)
	}
	var pw string
	if *pwStdin {
		line, _ := stdin.ReadString('\n')
		pw = strings.TrimRight(line, "\r\n")
	} else {
		pw = askPassword("Password")
	}
	caFile := *ca
	if caFile == "" {
		caFile = old.CAFile
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	token, err := runnercfg.Login(ctx, h, u, pw, caFile)
	if err != nil {
		die("login to %s failed: %v", h, err)
	}

	name, p := cfg.Ensure(*profile)
	p.Hub, p.NATS, p.Token, p.User, p.CAFile = h, runnercfg.NATSBase(h), token, u, caFile
	p.LoggedInAt = time.Now().Format(time.RFC3339)
	if err := cfg.Save(path); err != nil {
		die("cannot save %s: %v", path, err)
	}
	fmt.Printf("Logged in to %s as %s (profile %q).\n", h, u, name)
	if p.WorkingDir == "" {
		fmt.Println("Next: lucode-runner configure, then lucode-runner start")
	} else {
		fmt.Println("Next: lucode-runner start")
	}
}

func runLogout(args []string) {
	fs := flag.NewFlagSet("auth logout", flag.ExitOnError)
	profile := fs.String("profile", "", "profile (default: the current one)")
	_ = fs.Parse(args)
	cfg, path := loadConfig()
	p := cfg.Get(*profile)
	if p == nil || p.Token == "" {
		fmt.Println("Not logged in.")
		return
	}
	p.Token, p.LoggedInAt = "", ""
	if err := cfg.Save(path); err != nil {
		die("cannot save %s: %v", path, err)
	}
	fmt.Printf("Logged out of %s. A runner that is running keeps its connection until it stops.\n", p.Hub)
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("auth status", flag.ExitOnError)
	profile := fs.String("profile", "", "profile (default: the current one)")
	_ = fs.Parse(args)
	cfg, _ := loadConfig()
	name := cfg.Name(*profile)
	p := cfg.Get(*profile)
	if p == nil {
		fmt.Printf("Profile %q does not exist. Run: lucode-runner auth login\n", name)
		return
	}
	state := "logged in"
	if p.Token == "" {
		state = "not logged in"
	}
	fmt.Printf("profile      %s\nhub          %s\nuser         %s\nstate        %s\nworking dir  %s\n", name, p.Hub, p.User, state, p.WorkingDir)
}

func runConfigure(args []string) {
	fs := flag.NewFlagSet("configure", flag.ExitOnError)
	profile := fs.String("profile", "", "profile name (asked when not given)")
	wd := fs.String("working-dir", "", "folder the agents work in (asked when not given)")
	data := fs.String("data", "", "the runner's data folder (default: ~/.lucode-runner)")
	_ = fs.Parse(args)

	cfg, path := loadConfig()
	name := *profile
	if name == "" {
		name = ask("Profile name", cfg.Name(""))
	}
	_, p := cfg.Ensure(name)
	dir := *wd
	if dir == "" {
		def := p.WorkingDir
		if def == "" {
			if home, err := os.UserHomeDir(); err == nil {
				def = home
			}
		}
		dir = ask("Working directory path", def)
	}
	dir, err := filepath.Abs(expandHome(dir))
	if err != nil {
		die("%v", err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		if strings.ToLower(ask(dir+" does not exist. Create it? (y/n)", "n")) != "y" {
			die("not saved: %s is not a folder", dir)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			die("%v", err)
		}
	}
	p.WorkingDir = dir
	if *data != "" {
		p.DataDir = *data
	}
	if err := cfg.Save(path); err != nil {
		die("cannot save %s: %v", path, err)
	}
	fmt.Printf("Profile %q: agents work in %s.\n", name, dir)
	if p.Token == "" {
		fmt.Println("Next: lucode-runner auth login, then lucode-runner start")
	} else {
		fmt.Println("Next: lucode-runner start")
	}
}

func runStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	profile := fs.String("profile", "", "profile (default: the current one)")
	policy := fs.String("plugin-policy", plugins.PolicyStandard, "what plugins may do here: off, standard (no exec) or full")
	noRefresh := fs.Bool("no-claude-refresh", false, "do not renew the Claude logins of this machine in the background")
	_ = fs.Parse(args)

	cfg, _ := loadConfig()
	name := cfg.Name(*profile)
	p := cfg.Get(*profile)
	if p == nil {
		die("profile %q does not exist: run `lucode-runner auth login` and `lucode-runner configure`", name)
	}
	natsURL, err := p.NATSURL()
	if err != nil {
		die("profile %q: %v", name, err)
	}
	if p.WorkingDir == "" {
		fmt.Fprintln(os.Stderr, "lucode-runner: no working directory set (lucode-runner configure): using your home folder")
	}
	fmt.Printf("Starting profile %q: hub %s\n", name, p.Hub)
	runRunner(runnerOptions{NATSURL: natsURL, CAFile: p.CAFile, DataDir: p.Data(), WorkingDir: p.WorkingDir, PluginPolicy: *policy, NoClaudeRefresh: *noRefresh})
}

// expandHome turns a leading ~ into the user's home folder.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return os.ExpandEnv(p)
}
