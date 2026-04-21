package main

import (
	"bufio"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	apipkg "claude-orchestrator/internal/api"
	dbpkg "claude-orchestrator/internal/db"
	hookcfg "claude-orchestrator/internal/hookconfig"
	natspkg "claude-orchestrator/internal/natsbus"
	sessionpkg "claude-orchestrator/internal/session"
	webui "claude-orchestrator/web"
)

// embeddedHook is the lucodehook binary baked into this executable at build time.
// It is written to ~/.lucode/lucodehook during `lucode install`.
//
//go:embed embedded/lucodehook
var embeddedHook []byte

func main() {
	if len(os.Args) > 1 && os.Args[1] == "install" {
		runInstall()
		return
	}

	port := flag.Int("port", 8090, "HTTP listen port")
	natsURL := flag.String("nats-url", "nats://127.0.0.1:4222", "External NATS server URL")
	natsCreds := flag.String("nats-creds", "", "Path to NATS credentials file (.creds)")
	aiAPIURL  := flag.String("aiapi-url", "http://localhost:8091", "URL of the aiapi service (Claude CLI + Ollama + Anthropic executor)")
	authURL   := flag.String("auth-url", "http://localhost:8092", "URL of the centralized auth service")
	dataDir := flag.String("data", defaultDataDir(), "Directory for SQLite database")
	workingDir := flag.String("working-dir", "", "Base directory for workspace picker")
	hookBinary := flag.String("hook-binary", defaultHookBinary(), "Path to lucodehook binary")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}

	dbPath := filepath.Join(*dataDir, "lucode.db")
	db, err := dbpkg.Init(dbPath)
	if err != nil {
		log.Fatalf("db init: %v", err)
	}
	defer db.Close()

	// Connect to external NATS server.
	natsOpts := []nats.Option{
		nats.Timeout(10 * time.Second),
		nats.RetryOnFailedConnect(false),
	}
	if *natsCreds != "" {
		natsOpts = append(natsOpts, nats.UserCredentials(*natsCreds))
	}
	nc, err := nats.Connect(*natsURL, natsOpts...)
	if err != nil {
		log.Fatalf("nats connect %s: %v", *natsURL, err)
	}
	defer nc.Drain()
	log.Printf("[nats] connected — %s", *natsURL)

	bus := natspkg.NewBus(nc)
	bus.OnBroadcast = apipkg.BroadcastToWS

	backendBaseURL := fmt.Sprintf("http://localhost:%d", *port)
	claudeSettingsJSON, err := hookcfg.BuildJSON(*hookBinary, backendBaseURL)
	if err != nil {
		log.Fatalf("build claude settings: %v", err)
	}
	log.Printf("claude settings: hook=%s settings=%s", *hookBinary, claudeSettingsJSON)

	manager := sessionpkg.NewManager(db, bus, *natsURL, claudeSettingsJSON)
	manager.SetAIAPIURL(*aiAPIURL)
	if *natsCreds != "" {
		manager.SetNATSCreds(*natsCreds)
	}

	if err := bus.Start(); err != nil {
		log.Fatalf("nats bus: %v", err)
	}

	instanceID, _ := natspkg.LoadOrCreateInstanceID(*dataDir)
	if err := bus.StartDiscovery(natspkg.InstanceInfo{
		ID:   instanceID,
		Type: "lucode",
	}); err != nil {
		log.Printf("[discovery] start error: %v", err)
	}

	handler := apipkg.New(db, bus, manager, *workingDir)
	handler.SetAuthURL(*authURL)

	// Start RPC server after handler is created so it can serve remote requests
	if err := bus.StartRPCServer(apipkg.Wrap(handler)); err != nil {
		log.Printf("[rpc] start error: %v", err)
	}

	distFS, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		log.Fatalf("embed sub: %v", err)
	}

	httpHandler := apipkg.WrapWithSPA(handler, distFS)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("lucode listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, httpHandler); err != nil {
		log.Fatal(err)
	}
}

// ── install ──────────────────────────────────────────────────────────────────

func runInstall() {
	hr := strings.Repeat("─", 54)
	installDir := defaultDataDir()

	fmt.Println()
	fmt.Println(hr)
	fmt.Println("  LuCode installer")
	fmt.Println(hr)
	fmt.Println()

	// 1. Verify claude CLI is available.
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Println("  ✗  claude CLI not found in PATH.")
		fmt.Println()
		fmt.Println("  LuCode requires the Claude Code CLI to function.")
		fmt.Println("  Install it from:")
		fmt.Println()
		fmt.Println("    https://claude.ai/code")
		fmt.Println()
		fmt.Println("  Then re-run:  lucode install")
		fmt.Println()
		os.Exit(1)
	}
	fmt.Printf("  ✓  claude CLI found at %s\n", claudePath)
	fmt.Println()

	// 2. Create install directory.
	if err := os.MkdirAll(installDir, 0755); err != nil {
		log.Fatalf("install: mkdir %s: %v", installDir, err)
	}

	// 3. Extract embedded lucodehook.
	hookName := "lucodehook"
	if runtime.GOOS == "windows" {
		hookName = "lucodehook.exe"
	}
	hookDst := filepath.Join(installDir, hookName)
	if err := os.WriteFile(hookDst, embeddedHook, 0755); err != nil {
		log.Fatalf("install: write lucodehook: %v", err)
	}
	fmt.Printf("  ✓  extracted lucodehook  →  %s\n", hookDst)

	// 4. Copy lucode binary itself.
	self, err := os.Executable()
	if err != nil {
		log.Fatalf("install: cannot determine executable path: %v", err)
	}
	self, _ = filepath.EvalSymlinks(self)
	lucodeDst := filepath.Join(installDir, "lucode")
	if self == lucodeDst {
		fmt.Printf("  ✓  lucode already at      %s\n", lucodeDst)
	} else {
		if err := copyFile(self, lucodeDst, 0755); err != nil {
			log.Fatalf("install: copy lucode: %v", err)
		}
		fmt.Printf("  ✓  copied lucode         →  %s\n", lucodeDst)
	}

	// 5. Write ~/.lucode/settings.json.
	settingsPath := filepath.Join(installDir, "settings.json")
	if err := hookcfg.WriteSettings(settingsPath, hookDst, "http://localhost:8090"); err != nil {
		log.Fatalf("install: write settings: %v", err)
	}
	fmt.Printf("  ✓  wrote settings        →  %s\n", settingsPath)

	// 6. Interactive PATH setup.
	fmt.Println()
	fmt.Println(hr)
	setupPath(installDir)

	// 7. Done.
	fmt.Println()
	fmt.Println(hr)
	fmt.Println("  Install complete!")
	fmt.Println(hr)
	fmt.Println()
	fmt.Printf("  You can now run:\n\n")
	if runtime.GOOS == "windows" {
		fmt.Printf("    lucode.exe --port 8090 --working-dir %%USERPROFILE%%\n")
	} else {
		fmt.Printf("    lucode --port 8090 --working-dir ~/\n")
	}
	fmt.Println()
	fmt.Printf("  On first run lucode will use:\n")
	fmt.Printf("    %s\n", settingsPath)
	fmt.Println()
}

// setupPath offers to add installDir to the system PATH.
// On Windows it shows the setx command; on Unix it edits a shell rc file.
func setupPath(installDir string) {
	fmt.Println()

	if runtime.GOOS == "windows" {
		setupPathWindows(installDir)
		return
	}

	home, _ := os.UserHomeDir()

	candidates := []string{
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
	}

	var found []string
	for _, f := range candidates {
		if _, err := os.Stat(f); err == nil {
			found = append(found, f)
		}
	}

	exportLine := fmt.Sprintf(`export PATH="%s:$PATH"`, installDir)

	fmt.Println("  Add to your PATH?")
	fmt.Println()

	if len(found) == 0 {
		fmt.Println("  No shell config files found. Add manually:")
		fmt.Printf("    %s\n", exportLine)
		fmt.Println()
		return
	}

	fmt.Println("  Shell configs found:")
	fmt.Println()
	for i, f := range found {
		short := strings.Replace(f, home, "~", 1)
		fmt.Printf("    [%d] %s\n", i+1, short)
	}
	fmt.Println("    [0] Skip")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("  Choice [1]: ")
	raw, _ := reader.ReadString('\n')
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "1"
	}

	if raw == "0" {
		fmt.Println()
		fmt.Println("  Skipped. Add manually:")
		fmt.Printf("    %s\n", exportLine)
		fmt.Println()
		return
	}

	idx := 0
	fmt.Sscanf(raw, "%d", &idx)
	if idx < 1 || idx > len(found) {
		fmt.Println()
		fmt.Println("  Invalid choice. Add manually:")
		fmt.Printf("    %s\n", exportLine)
		fmt.Println()
		return
	}

	target := found[idx-1]
	short := strings.Replace(target, home, "~", 1)

	// Check if already present.
	existing, _ := os.ReadFile(target)
	if strings.Contains(string(existing), installDir) {
		fmt.Printf("\n  ✓  %s already contains %s\n", short, installDir)
		fmt.Printf("     (no changes made)\n")
		return
	}

	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("\n  ✗  Could not write to %s: %v\n", short, err)
		fmt.Printf("     Add manually: %s\n", exportLine)
		return
	}
	defer f.Close()

	block := fmt.Sprintf("\n# lucode\n%s\n", exportLine)
	if _, err := f.WriteString(block); err != nil {
		fmt.Printf("\n  ✗  Write error: %v\n", err)
		fmt.Printf("     Add manually: %s\n", exportLine)
		return
	}

	fmt.Printf("\n  ✓  Added to %s\n", short)
	fmt.Printf("\n  Reload with:  source %s\n", short)
}

// setupPathWindows shows Windows-specific PATH instructions.
// It prints the setx command and asks the user if they want to run it.
func setupPathWindows(installDir string) {
	setxCmd := fmt.Sprintf(`setx PATH "%s;%%PATH%%"`, installDir)

	fmt.Println("  Add to your PATH?")
	fmt.Println()
	fmt.Printf("  Install directory: %s\n", installDir)
	fmt.Println()
	fmt.Println("  Run this in an Administrator Command Prompt:")
	fmt.Println()
	fmt.Printf("    %s\n", setxCmd)
	fmt.Println()
	fmt.Println("  Or: Settings → System → Advanced system settings")
	fmt.Println("      → Environment Variables → Path → New")
	fmt.Printf("      → paste: %s\n", installDir)
	fmt.Println()
}

// ── helpers ───────────────────────────────────────────────────────────────────

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lucode"
	}
	return filepath.Join(home, ".lucode")
}

func defaultHookBinary() string {
	// In dev (air), the hook is built to ./tmp/lucodehook
	if _, err := os.Stat("./tmp/lucodehook"); err == nil {
		abs, _ := filepath.Abs("./tmp/lucodehook")
		return abs
	}
	// In production, use ~/.lucode/lucodehook
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lucode", "lucodehook")
}
