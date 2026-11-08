package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"claude-orchestrator/internal/plugins"

	"github.com/dop251/goja"
)

// The runner target's SDK. The hub set the workspace in a signed message; a plugin has no say in
// it. This machine holds the user's code and credentials, so everything here is narrower than on
// the hub.

const (
	maxFSRead      = 5 << 20
	maxFSWrite     = 20 << 20
	maxListEntries = 5000
	maxExecOutput  = 256 << 10 // of each stream: both have to fit in one sealed answer to the hub
	defaultExec    = time.Minute
	gitTimeout     = 30 * time.Second
)

var secretName = regexp.MustCompile(`(?i)^(\.git|\.env.*|.*\.pem|.*\.key|.*\.p12|.*\.pfx|id_rsa.*|id_dsa.*|id_ecdsa.*|id_ed25519.*|\.lucode.*|\.ssh|\.aws|\.gnupg|\.npmrc|\.netrc|\.pgpass|\.git-credentials|\.docker|\.kube|credentials(\..*)?)$`)
var shortName = regexp.MustCompile(`~[0-9]+(\.|$)`) // a Windows 8.3 alias of a longer name

type invocationEnv struct {
	agent       *Agent
	perms       plugins.Permissions
	execRefusal string
	payload     plugins.InvokePayload
	secret      []byte
}

// checkWorkspace refuses a workspace that cannot be meant: the hub sets it, and it is never a root.
func (e *invocationEnv) checkWorkspace() error {
	ws := e.payload.Workspace
	if ws == "" {
		return nil // fs, git and exec throw; the rest of the plugin works
	}
	if !filepath.IsAbs(ws) {
		return errors.New("the workspace must be an absolute path")
	}
	clean := filepath.Clean(ws)
	if clean == filepath.Dir(clean) {
		return errors.New("a whole disk cannot be a workspace")
	}
	return nil
}

func (e *invocationEnv) workspace(s *plugins.Scope) string {
	ws := e.payload.Workspace
	if ws == "" {
		s.Throw("this invocation has no workspace: the session or card it runs for has no folder on this runner")
	}
	real, err := filepath.EvalSymlinks(ws)
	if err != nil {
		s.Throw("the workspace does not exist on this runner")
	}
	return real
}

func (e *invocationEnv) modules() []plugins.Module {
	return []plugins.Module{e.fsModule(), e.gitModule(), e.execModule(), e.serverModule()}
}

// SafeRelPath checks a workspace-relative path as text, before the disk is touched.
func SafeRelPath(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) || strings.Contains(rel, `\`) || strings.Contains(rel, ":") {
		return "", fmt.Errorf("%q is not a path inside the workspace", rel)
	}
	if rel == "" {
		rel = "."
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%q is not a path inside the workspace", rel)
	}
	clean := path.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%q leaves the workspace", rel)
	}
	if clean == "." {
		return clean, nil
	}
	for _, part := range strings.Split(clean, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || shortName.MatchString(part) {
			return "", fmt.Errorf("%q is not a name plugins may use", rel)
		}
		if secretName.MatchString(part) {
			return "", fmt.Errorf("%q is refused: plugins do not read or write .git or secret files", rel)
		}
	}
	return clean, nil
}

// checkNoJunction refuses a Windows junction anywhere on the way. EvalSymlinks does not follow
// them, so to it a junction looks like a folder inside the workspace, while it can lead anywhere,
// and making one takes no privilege.
func checkNoJunction(root, clean string) error {
	cur := root
	for _, part := range strings.Split(clean, "/") {
		if part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return nil // nothing beyond here exists yet
		}
		if info.Mode()&os.ModeIrregular != 0 {
			return errors.New("goes through a junction or another special folder, which plugins do not follow")
		}
	}
	return nil
}

// ResolvePath gives the path to use under the workspace root, once links are followed. The
// target of a link is held to the same rules as the name that led to it.
func ResolvePath(root, rel string) (string, error) {
	clean, err := SafeRelPath(rel)
	if err != nil {
		return "", err
	}
	if clean == "." {
		return clean, nil
	}
	if err := checkNoJunction(root, clean); err != nil {
		return "", fmt.Errorf("%q %w", rel, err)
	}
	// the deepest part that exists decides where this really is
	cur := filepath.Join(root, filepath.FromSlash(clean))
	rest := ""
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if real, err = filepath.Abs(real); err != nil {
				return "", err
			}
			inside, err := filepath.Rel(root, real)
			if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) || filepath.IsAbs(inside) {
				return "", fmt.Errorf("%q leaves the workspace through a link", rel)
			}
			full := filepath.ToSlash(filepath.Join(inside, rest))
			if _, err := SafeRelPath(full); err != nil {
				return "", fmt.Errorf("%q is refused: it leads to a place plugins do not read or write", rel)
			}
			if err := checkNoJunction(root, full); err != nil {
				return "", fmt.Errorf("%q %w", rel, err)
			}
			return clean, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%q cannot be followed", rel)
		}
		parent := filepath.Dir(cur)
		if parent == cur || len(parent) < len(root) {
			return "", fmt.Errorf("%q leaves the workspace", rel)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func (e *invocationEnv) fsModule() plugins.Module {
	needRead := func(s *plugins.Scope) {
		if e.perms.FS == "" {
			s.Throw("this needs the fs permission, which this plugin was not granted")
		}
	}
	open := func(s *plugins.Scope, rel string) (*os.Root, string) {
		needRead(s)
		root := e.workspace(s)
		clean, err := ResolvePath(root, rel)
		if err != nil {
			s.Throw("%s", err.Error())
		}
		r, err := os.OpenRoot(root)
		if err != nil {
			s.Throw("the workspace cannot be opened")
		}
		return r, clean
	}
	return plugins.Module{Name: "fs", Funcs: map[string]plugins.Func{
		"read": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			r, rel := open(s, s.Str(c, 0, "path"))
			defer r.Close()
			f, err := r.Open(rel)
			if err != nil {
				s.Throw("%q cannot be read", rel)
			}
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, maxFSRead+1))
			if err != nil {
				s.Throw("%q cannot be read", rel)
			}
			if len(data) > maxFSRead {
				s.Throw("%q is larger than %d MB", rel, maxFSRead>>20)
			}
			return s.VM.ToValue(string(data))
		},
		"write": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			if e.perms.FS != "write" {
				s.Throw("this needs the fs write permission, which this plugin was not granted")
			}
			text := s.Str(c, 1, "text")
			if len(text) > maxFSWrite {
				s.Throw("this is larger than %d MB", maxFSWrite>>20)
			}
			r, rel := open(s, s.Str(c, 0, "path"))
			defer r.Close()
			if dir := path.Dir(rel); dir != "." {
				if err := r.MkdirAll(dir, 0o755); err != nil {
					s.Throw("%q cannot be created", dir)
				}
			}
			f, err := r.OpenFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				s.Throw("%q cannot be written", rel)
			}
			defer f.Close()
			if _, err := f.WriteString(text); err != nil {
				s.Throw("%q cannot be written", rel)
			}
			return goja.Undefined()
		},
		"list": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			rel := "."
			if a := c.Argument(0); !goja.IsUndefined(a) && !goja.IsNull(a) && a.String() != "" {
				rel = a.String()
			}
			r, rel := open(s, rel)
			defer r.Close()
			f, err := r.Open(rel)
			if err != nil {
				s.Throw("%q cannot be listed", rel)
			}
			defer f.Close()
			entries, err := f.ReadDir(maxListEntries)
			if err != nil && !errors.Is(err, io.EOF) {
				s.Throw("%q cannot be listed", rel)
			}
			out := []any{}
			for _, entry := range entries {
				if secretName.MatchString(entry.Name()) {
					continue // not even their names
				}
				out = append(out, map[string]any{"name": entry.Name(), "dir": entry.IsDir()})
			}
			return s.JS(out)
		},
		"exists": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			r, rel := open(s, s.Str(c, 0, "path"))
			defer r.Close()
			_, err := r.Stat(rel)
			return s.VM.ToValue(err == nil)
		},
		"stat": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			r, rel := open(s, s.Str(c, 0, "path"))
			defer r.Close()
			info, err := r.Stat(rel)
			if err != nil {
				return goja.Null()
			}
			return s.JS(map[string]any{"size": float64(info.Size()), "dir": info.IsDir(), "modified": info.ModTime().UTC().Format(time.RFC3339)})
		},
	}}
}

// ---- running a program ----

// scrubbedEnv is what a child gets: what programs need to run, and no tokens.
func scrubbedEnv() []string {
	keep := []string{"PATH", "PATHEXT", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "TEMP", "TMP", "TMPDIR", "HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "LANG", "LC_ALL"}
	out := []string{}
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// capped keeps the first bytes of a stream and drops the rest. The buffer is a field, not embedded:
// its ReadFrom would let io.Copy go around Write and the limit.
type capped struct {
	buf   bytes.Buffer
	limit int
	cut   bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room < len(p) {
		c.cut = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *capped) String() string { return c.buf.String() }

// runProgram starts bin with no shell in between, and kills its whole tree at the deadline.
func runProgram(ctx context.Context, timeout time.Duration, bin, cwd string, argv []string) (code int, stdout, stderr *capped, cut bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Dir = cwd
	cmd.Env = scrubbedEnv()
	cmd.Stdin = nil
	stdout, stderr = &capped{limit: maxExecOutput}, &capped{limit: maxExecOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 2 * time.Second
	prepareTree(cmd)
	cmd.Cancel = func() error { killTree(cmd); return nil }
	err = cmd.Run()
	if ctx.Err() != nil {
		killTree(cmd) // whatever is left of the tree, even if the parent ended first
		return -1, stdout, stderr, stdout.cut || stderr.cut, fmt.Errorf("the command ran out of time (%s) and was killed", timeout.Round(time.Millisecond))
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), stdout, stderr, stdout.cut || stderr.cut, nil
	}
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return -1, stdout, stderr, false, errors.New("the command could not be started")
	}
	return 0, stdout, stderr, stdout.cut || stderr.cut, nil
}

func (e *invocationEnv) execModule() plugins.Module {
	return plugins.Module{Name: "exec", Funcs: map[string]plugins.Func{
		"run": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			if e.execRefusal != "" {
				s.Throw("%s", e.execRefusal)
			}
			if e.perms.Exec == nil {
				s.Throw("this needs the exec permission, which this plugin was not granted")
			}
			root := e.workspace(s)
			name := s.Str(c, 0, "cmd")
			if !commandAllowed(name, e.perms.Exec.Commands) {
				s.Throw("the command %q is not in this plugin's exec permission", name)
			}
			bin, err := exec.LookPath(name)
			if err != nil {
				s.Throw("the command %q is not installed on this runner", name)
			}
			if err := checkBinary(bin, root); err != nil {
				s.Throw("%s", err.Error())
			}
			argv := []string{}
			if a := s.Plain(c.Argument(1)); a != nil {
				list, ok := a.([]any)
				if !ok {
					s.Throw("argv must be a list of strings")
				}
				for _, x := range list {
					text, ok := x.(string)
					if !ok || strings.ContainsRune(text, 0) {
						s.Throw("argv must be a list of strings")
					}
					argv = append(argv, text)
				}
			}
			o := s.Obj(c, 2)
			timeout := defaultExec
			if ms, ok := o["timeoutMs"].(float64); ok && ms > 0 {
				timeout = time.Duration(ms) * time.Millisecond
			}
			cwd := root
			if rel, ok := o["cwd"].(string); ok && rel != "" {
				clean, err := ResolvePath(root, rel)
				if err != nil {
					s.Throw("%s", err.Error())
				}
				cwd = filepath.Join(root, filepath.FromSlash(clean))
			}
			res := plugins.Wait(s, func(ctx context.Context) (map[string]any, error) {
				code, out, errOut, cut, err := runProgram(ctx, timeout, bin, cwd, argv)
				if err != nil {
					return nil, err
				}
				return map[string]any{"code": float64(code), "stdout": out.String(), "stderr": errOut.String(), "truncated": cut}, nil
			})
			return s.JS(res)
		},
	}}
}

// commandAllowed matches a base name: a plugin never gives a path.
func commandAllowed(name string, allowed []string) bool {
	if strings.ContainsAny(name, `/\:`) || name == "" {
		return false
	}
	cmp := name
	if runtime.GOOS == "windows" {
		cmp = strings.TrimSuffix(strings.ToLower(cmp), ".exe")
	}
	for _, a := range allowed {
		if runtime.GOOS == "windows" {
			a = strings.TrimSuffix(strings.ToLower(a), ".exe")
		}
		if a == cmp {
			return true
		}
	}
	return false
}

// checkBinary refuses what the name resolved to if a repository could have planted it, or if
// Windows would run it through cmd.exe, which is a shell.
func checkBinary(bin, workspace string) error {
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return errors.New("the command cannot be found")
	}
	if abs, err := filepath.Abs(real); err == nil {
		if rel, err := filepath.Rel(workspace, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("this command resolves to a file inside the workspace, and is refused")
		}
	}
	ext := strings.ToLower(filepath.Ext(real))
	if ext == ".bat" || ext == ".cmd" {
		return fmt.Errorf("%s is a batch file, which needs a shell, and plugins run no shell", filepath.Base(real))
	}
	return nil
}

// ---- git ----

var gitRefRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,200}$`)

// the settings a repository could use to make a read run something
var gitSafe = []string{
	"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "-c", "core.pager=cat",
	"-c", "core.sshCommand=false", "-c", "credential.helper=", "-c", "protocol.allow=never", "-c", "core.attributesFile=",
	"--no-pager",
}

// secretPathspecs keeps out of a diff what secretName keeps out of fs.
var secretPathspecs = func() []string {
	out := []string{}
	for _, glob := range []string{
		".env*", "*.pem", "*.key", "*.p12", "*.pfx", "id_rsa*", "id_dsa*", "id_ecdsa*", "id_ed25519*", ".lucode*",
		".npmrc", ".netrc", ".pgpass", ".git-credentials", "credentials", "credentials.*",
	} {
		out = append(out, ":(exclude,glob,icase)**/"+glob)
	}
	for _, dir := range []string{".ssh", ".aws", ".gnupg", ".docker", ".kube"} {
		out = append(out, ":(exclude,glob,icase)**/"+dir+"/**")
	}
	return out
}()

// visibleStatus takes secret files out of git status: their names are not for a plugin either.
func visibleStatus(text string) string {
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, line := range lines {
		if len(line) > 3 && !strings.HasPrefix(line, "## ") {
			secret := false
			for _, name := range strings.Split(line[3:], " -> ") {
				if _, err := SafeRelPath(strings.Trim(name, `"`)); err != nil {
					secret = true
				}
			}
			if secret {
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (e *invocationEnv) gitModule() plugins.Module {
	run := func(s *plugins.Scope, args ...string) string {
		if e.perms.Git == "" {
			s.Throw("this needs the git permission, which this plugin was not granted")
		}
		root := e.workspace(s)
		bin, err := exec.LookPath("git")
		if err != nil {
			s.Throw("git is not installed on this runner")
		}
		argv := append(append([]string{"-C", root}, gitSafe...), args...)
		return plugins.Wait(s, func(ctx context.Context) (string, error) {
			code, out, errOut, _, err := runProgram(ctx, gitTimeout, bin, root, argv)
			if err != nil {
				return "", err
			}
			if code != 0 {
				text := strings.TrimSpace(errOut.String())
				return "", fmt.Errorf("git: %s", plugins.Scrub(text))
			}
			return out.String(), nil
		})
	}
	return plugins.Module{Name: "git", Funcs: map[string]plugins.Func{
		"status": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			return s.VM.ToValue(visibleStatus(run(s, "status", "--porcelain=v1", "--branch")))
		},
		"branch": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			return s.VM.ToValue(strings.TrimSpace(run(s, "rev-parse", "--abbrev-ref", "HEAD")))
		},
		"log": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			limit := 20
			if n, ok := s.Obj(c, 0)["limit"].(float64); ok && n > 0 && n <= 500 {
				limit = int(n)
			}
			text := run(s, "log", fmt.Sprintf("-%d", limit), "--format=%H%x1f%an%x1f%aI%x1f%s")
			out := []any{}
			for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
				f := strings.Split(line, "\x1f")
				if len(f) == 4 {
					out = append(out, map[string]any{"sha": f[0], "author": f[1], "date": f[2], "subject": f[3]})
				}
			}
			return s.JS(out)
		},
		"diff": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			o := s.Obj(c, 0)
			args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color"}
			if staged, _ := o["staged"].(bool); staged {
				args = append(args, "--cached")
			}
			if base, ok := o["base"].(string); ok && base != "" {
				if !gitRefRe.MatchString(base) {
					s.Throw("%q is not a branch or a commit", base)
				}
				args = append(args, base)
			}
			args = append(args, "--", ".")
			if rel, ok := o["path"].(string); ok && rel != "" {
				clean, err := SafeRelPath(rel)
				if err != nil {
					s.Throw("%s", err.Error())
				}
				args[len(args)-1] = clean
			}
			args = append(args, secretPathspecs...)
			return s.VM.ToValue(run(s, args...))
		},
	}}
}

// ---- server ----

var rpcNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func (e *invocationEnv) serverModule() plugins.Module {
	return plugins.Module{Name: "server", Funcs: map[string]plugins.Func{
		"call": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			if e.payload.CallSubject == "" || e.payload.CallToken == "" {
				s.Throw("this invocation cannot reach the hub")
			}
			name := s.Str(c, 0, "rpcName")
			if !rpcNameRe.MatchString(name) {
				s.Throw("%q is not an rpc name", name)
			}
			var n [16]byte
			if _, err := rand.Read(n[:]); err != nil {
				s.Throw("this call cannot be sent")
			}
			call := plugins.ServerCall{
				TS: time.Now().UnixMilli(), Nonce: hex.EncodeToString(n[:]),
				Token: e.payload.CallToken, RPC: name, Args: s.Plain(c.Argument(1)),
			}
			raw, nonce, err := plugins.SealCall(e.secret, e.agent.o.RunnerID, call)
			if err != nil {
				s.Throw("these arguments cannot be sent")
			}
			if len(raw) > plugins.MaxWireBytes {
				s.Throw("these arguments are larger than %d KB, the most that reaches the hub at once", plugins.MaxWireBytes>>10)
			}
			res := plugins.Wait(s, func(ctx context.Context) (any, error) {
				msg, err := e.agent.o.NATS.RequestWithContext(ctx, e.payload.CallSubject, raw)
				if err != nil {
					return nil, errors.New("the hub did not answer")
				}
				reply, err := plugins.OpenCallReply(e.secret, e.agent.o.RunnerID, nonce, msg.Data)
				if err != nil {
					return nil, err
				}
				if !reply.OK {
					return nil, errors.New(reply.Error)
				}
				return reply.Value, nil
			})
			return s.JS(res)
		},
	}}
}
