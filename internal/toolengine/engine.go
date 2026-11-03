package toolengine

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dop251/goja"
)

const defaultTimeout = 10 * time.Second

// Execute runs user-defined JavaScript code in a sandboxed Goja VM.
// The code receives `args` as the parsed arguments object and has access
// to an injected SDK with fs, http, and log modules.
// Returns the JSON-serializable result or an error.
func Execute(code string, args json.RawMessage, workDir string) (any, error) {
	vm := goja.New()

	// Set up timeout / kill switch. Code that drives the browser gets longer.
	timeout := defaultTimeout
	if strings.Contains(code, "cdp.") {
		timeout = browserTimeout
	}
	timer := time.AfterFunc(timeout, func() {
		vm.Interrupt("execution timeout exceeded")
	})
	defer timer.Stop()

	// Inject parsed arguments as `args`
	var argsObj any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsObj); err != nil {
			argsObj = map[string]any{}
		}
	} else {
		argsObj = map[string]any{}
	}
	vm.Set("args", argsObj)

	// Inject SDK modules
	injectFS(vm, workDir)
	injectHTTP(vm)
	injectLog(vm)
	injectUtils(vm)
	injectCDP(vm)

	// Wrap user code in a function to capture return value
	wrappedCode := fmt.Sprintf(`(function() { %s })()`, code)

	val, err := vm.RunString(wrappedCode)
	if err != nil {
		if interrupted, ok := err.(*goja.InterruptedError); ok {
			return nil, fmt.Errorf("timeout: %s", interrupted.Value())
		}
		return nil, fmt.Errorf("execution error: %w", err)
	}

	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return nil, nil
	}

	return val.Export(), nil
}

// injectFS provides file system operations scoped to workDir.
func injectFS(vm *goja.Runtime, workDir string) {
	resolvePath := func(p string) (string, error) {
		resolved := p
		if !filepath.IsAbs(p) {
			resolved = filepath.Join(workDir, p)
		}
		resolved = filepath.Clean(resolved)
		// Security: prevent path traversal outside workDir
		if workDir != "" && !strings.HasPrefix(resolved, filepath.Clean(workDir)) {
			return "", fmt.Errorf("path %q is outside working directory", p)
		}
		return resolved, nil
	}

	vm.Set("fs", map[string]any{
		"readFile": func(path string) (string, error) {
			resolved, err := resolvePath(path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
		"writeFile": func(path string, content string) error {
			resolved, err := resolvePath(path)
			if err != nil {
				return err
			}
			dir := filepath.Dir(resolved)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			return os.WriteFile(resolved, []byte(content), 0644)
		},
		"readDir": func(path string) ([]string, error) {
			resolved, err := resolvePath(path)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(resolved)
			if err != nil {
				return nil, err
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return names, nil
		},
		"exists": func(path string) bool {
			resolved, err := resolvePath(path)
			if err != nil {
				return false
			}
			_, err = os.Stat(resolved)
			return err == nil
		},
	})
}

// injectHTTP provides basic HTTP request capability.
func injectHTTP(vm *goja.Runtime) {
	vm.Set("http", map[string]any{
		"fetch": func(url string, options ...map[string]any) (map[string]any, error) {
			method := "GET"
			var body io.Reader
			var headers map[string]string

			if len(options) > 0 {
				opts := options[0]
				if m, ok := opts["method"].(string); ok {
					method = strings.ToUpper(m)
				}
				if b, ok := opts["body"].(string); ok {
					body = strings.NewReader(b)
				}
				if h, ok := opts["headers"].(map[string]any); ok {
					headers = make(map[string]string)
					for k, v := range h {
						if s, ok := v.(string); ok {
							headers[k] = s
						}
					}
				}
			}

			client := &http.Client{Timeout: 10 * time.Second}
			req, err := http.NewRequest(method, url, body)
			if err != nil {
				return nil, err
			}
			for k, v := range headers {
				req.Header.Set(k, v)
			}

			resp, err := client.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()

			respBody, _ := io.ReadAll(resp.Body)

			result := map[string]any{
				"status":     resp.StatusCode,
				"statusText": resp.Status,
				"body":       string(respBody),
			}

			// Try to parse body as JSON
			var jsonBody any
			if err := json.Unmarshal(respBody, &jsonBody); err == nil {
				result["json"] = jsonBody
			}

			return result, nil
		},
	})
}

// injectLog provides console-like logging.
func injectLog(vm *goja.Runtime) {
	vm.Set("log", map[string]any{
		"info": func(args ...any) {
			log.Printf("[toolengine] INFO: %v", args)
		},
		"warn": func(args ...any) {
			log.Printf("[toolengine] WARN: %v", args)
		},
		"error": func(args ...any) {
			log.Printf("[toolengine] ERROR: %v", args)
		},
	})
}

// injectUtils provides JSON and other helpers.
func injectUtils(vm *goja.Runtime) {
	vm.Set("JSON", map[string]any{
		"parse": func(s string) (any, error) {
			var v any
			err := json.Unmarshal([]byte(s), &v)
			return v, err
		},
		"stringify": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
	})
}
