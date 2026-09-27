package scripts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Runner runs script functions (§4.15.5): in a venv built from the script's
// requirements.txt and cached by content, with a scrubbed environment that
// carries only sessile's own network settings and this script's settings, a
// timeout that kills the whole process group, capped output, and every
// secret redacted from what comes back.
type Runner struct {
	DataDir string
	Log     *slog.Logger
	// Python is the interpreter venvs are made from.
	Python string
	// GitToken resolves a setting mapped to a Git account (§4.15.3).
	GitToken func(userID, host string) (string, bool)

	mu     sync.Mutex
	venvs  map[string]*venvBuild
	checks map[string]CheckResult // by userID/name
}

type venvBuild struct {
	done chan struct{}
	err  error
}

// VenvState is a script's venv as the Scripts page shows it.
type VenvState string

const (
	VenvReady     VenvState = "ready"
	VenvPreparing VenvState = "preparing"
	VenvMissing   VenvState = "missing"
	VenvFailed    VenvState = "failed"
)

// CheckResult is the last Test connection of a script (§4.15.2).
type CheckResult struct {
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Result is one function call's outcome.
type Result struct {
	Output   json.RawMessage `json:"output"`
	Stderr   string          `json:"stderr,omitempty"`
	Duration time.Duration   `json:"-"`
}

const (
	maxStdout = 256 << 10
	maxStderr = 64 << 10
)

// NewRunner returns a Runner.
func NewRunner(dataDir string, log *slog.Logger) *Runner {
	return &Runner{DataDir: dataDir, Log: log, Python: "python3",
		venvs: map[string]*venvBuild{}, checks: map[string]CheckResult{}}
}

func requirements(dir string) []byte {
	b, _ := os.ReadFile(filepath.Join(dir, "requirements.txt"))
	return b
}

// venvKey is the content key of §4.15.5: identical requirements share one
// venv across scripts and users.
func venvKey(runtime string, reqs []byte) string {
	h := sha256.New()
	h.Write([]byte(runtime))
	h.Write([]byte{0})
	h.Write(bytes.TrimSpace(reqs))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func (r *Runner) venvDir(key string) string {
	return filepath.Join(r.DataDir, "cache", "venvs", key)
}

// VenvStatus reports a script's venv without building it.
func (r *Runner) VenvStatus(m Meta, scriptDir string) (VenvState, string) {
	key := venvKey(m.Runtime, requirements(scriptDir))
	r.mu.Lock()
	b, building := r.venvs[key]
	r.mu.Unlock()
	if building {
		select {
		case <-b.done:
			if b.err != nil {
				return VenvFailed, b.err.Error()
			}
		default:
			return VenvPreparing, ""
		}
	}
	if _, err := os.Stat(filepath.Join(r.venvDir(key), ".ready")); err == nil {
		return VenvReady, ""
	}
	return VenvMissing, ""
}

// Prepare builds a script's venv in the background (after an install).
func (r *Runner) Prepare(m Meta, scriptDir string) {
	go func() {
		if _, err := r.venv(context.Background(), m, scriptDir, false); err != nil && r.Log != nil {
			r.Log.Warn("prepare script venv failed", "script", m.Name, "err", err)
		}
	}()
}

// Rebuild drops a script's venv and builds it again.
func (r *Runner) Rebuild(ctx context.Context, m Meta, scriptDir string) error {
	_, err := r.venv(ctx, m, scriptDir, true)
	return err
}

// venv returns the python of a script's venv, building it once if needed.
// Builds are single-flight per key: concurrent callers wait for the one build.
func (r *Runner) venv(ctx context.Context, m Meta, scriptDir string, rebuild bool) (string, error) {
	reqs := requirements(scriptDir)
	key := venvKey(m.Runtime, reqs)
	dir := r.venvDir(key)
	python := filepath.Join(dir, "bin", "python")

	for {
		r.mu.Lock()
		b, inFlight := r.venvs[key]
		if inFlight {
			select {
			case <-b.done:
				delete(r.venvs, key) // finished: fall through to a fresh look
				inFlight = false
			default:
			}
		}
		if inFlight {
			r.mu.Unlock()
			select {
			case <-b.done:
				if b.err != nil {
					return "", b.err
				}
				if !rebuild {
					return python, nil
				}
				rebuild = false
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if !rebuild {
			if _, err := os.Stat(filepath.Join(dir, ".ready")); err == nil {
				r.mu.Unlock()
				return python, nil
			}
		}
		b = &venvBuild{done: make(chan struct{})}
		r.venvs[key] = b
		r.mu.Unlock()

		b.err = r.build(dir, reqs)
		close(b.done)
		if b.err != nil {
			return "", b.err
		}
		return python, nil
	}
}

func (r *Runner) build(dir string, reqs []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, r.Python, "-m", "venv", dir)
	cmd.Env = r.baseEnv("")
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("create venv: %w: %s", err, tail(out.String(), 2000))
	}
	if len(bytes.TrimSpace(reqs)) > 0 {
		reqPath := filepath.Join(dir, "requirements.txt")
		if err := os.WriteFile(reqPath, reqs, 0o600); err != nil {
			return err
		}
		out.Reset()
		pip := exec.CommandContext(ctx, filepath.Join(dir, "bin", "python"), "-m", "pip", "install",
			"--disable-pip-version-check", "--no-input", "-r", reqPath)
		pip.Env = r.baseEnv("")
		pip.Stdout, pip.Stderr = &out, &out
		if err := pip.Run(); err != nil {
			return fmt.Errorf("pip install: %w: %s", err, tail(out.String(), 2000))
		}
	}
	return os.WriteFile(filepath.Join(dir, ".ready"), nil, 0o600)
}

// passthrough is sessile's own network environment, handed to scripts and
// pip as it is (§9): sessile is expected to run with its proxies set.
var passthrough = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"ALL_PROXY", "all_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE",
	"PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL", "PIP_TRUSTED_HOST", "PIP_CERT",
}

func (r *Runner) baseEnv(home string) []string {
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "PYTHONDONTWRITEBYTECODE=1"}
	if home != "" {
		env = append(env, "HOME="+home)
	}
	for _, k := range passthrough {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Env resolves a script's settings to its environment and the secrets to
// redact. Git-mapped settings take the Git account's token.
func (r *Runner) Env(userID string, m Meta, st Settings) (env []string, secrets map[string]string) {
	secrets = map[string]string{}
	for _, d := range m.Settings {
		v := st.Values[d.Name]
		if host := st.Git[d.Name]; host != "" && r.GitToken != nil {
			if tok, ok := r.GitToken(userID, host); ok {
				v = tok
			}
		}
		if v == "" {
			continue
		}
		env = append(env, d.Name+"="+v)
		if d.Type == "secret" {
			secrets[d.Name] = v
		}
	}
	return env, secrets
}

// ErrScript is a script that ran and failed, as opposed to one that couldn't
// be run at all.
type ErrScript struct{ Msg string }

func (e *ErrScript) Error() string { return e.Msg }

// Run calls one function of an installed script.
func (r *Runner) Run(ctx context.Context, userID string, m Meta, scriptDir string, st Settings,
	function string, input json.RawMessage) (Result, error) {
	f, ok := m.Function(function)
	if !ok {
		return Result{}, fmt.Errorf("%s has no function %q", m.Name, function)
	}
	if missing := Missing(m, st); len(missing) > 0 {
		return Result{}, fmt.Errorf("%s needs setup: %s", m.Name, strings.Join(missing, ", "))
	}
	if err := ValidateInput(f, input); err != nil {
		return Result{}, err
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	python, err := r.venv(ctx, m, scriptDir, false)
	if err != nil {
		return Result{}, fmt.Errorf("prepare the venv: %w", err)
	}
	home := filepath.Join(r.DataDir, "cache", "script-home", userID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return Result{}, err
	}
	settingsEnv, secrets := r.Env(userID, m, st)

	cmd := exec.Command(python, filepath.Join(scriptDir, filepath.FromSlash(m.Entry)), function)
	cmd.Dir = scriptDir
	cmd.Env = append(r.baseEnv(home), settingsEnv...)
	cmd.Stdin = bytes.NewReader(input)
	stdout := &capped{max: maxStdout}
	stderr := &capped{max: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// Its own process group, so the timeout takes whatever it started too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start %s: %w", m.Name, err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	timeout := time.Duration(m.TimeoutSeconds) * time.Second
	var runErr error
	select {
	case runErr = <-waited:
	case <-time.After(timeout):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waited
		return Result{Stderr: redact(stderr.String(), secrets)}, &ErrScript{Msg: fmt.Sprintf("%s.%s timed out after %s", m.Name, function, timeout)}
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waited
		return Result{}, ctx.Err()
	}

	res := Result{Stderr: redact(stderr.String(), secrets), Duration: time.Since(start)}
	out := redact(stdout.String(), secrets)
	if runErr != nil {
		return res, &ErrScript{Msg: fmt.Sprintf("%s.%s failed (%v): %s", m.Name, function, runErr, tail(res.Stderr, 1000))}
	}
	if stdout.over {
		return res, &ErrScript{Msg: fmt.Sprintf("%s.%s printed more than %d KiB", m.Name, function, maxStdout>>10)}
	}
	out = strings.TrimSpace(out)
	if !json.Valid([]byte(out)) {
		return res, &ErrScript{Msg: fmt.Sprintf("%s.%s printed something that isn't JSON: %s", m.Name, function, tail(out, 300))}
	}
	res.Output = json.RawMessage(out)
	return res, nil
}

// Check runs a script's check function and remembers the result.
func (r *Runner) Check(ctx context.Context, userID string, m Meta, scriptDir string, st Settings) CheckResult {
	res := CheckResult{At: time.Now().UTC()}
	if m.Check == "" {
		res.OK = true
		res.Message = "This script has no check function."
	} else if out, err := r.Run(ctx, userID, m, scriptDir, st, m.Check, nil); err != nil {
		res.Message = err.Error()
	} else {
		res.OK = true
		res.Message = tail(string(out.Output), 500)
	}
	r.mu.Lock()
	r.checks[userID+"/"+m.Name] = res
	r.mu.Unlock()
	return res
}

// LastCheck returns a script's last check, if any.
func (r *Runner) LastCheck(userID, name string) (CheckResult, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.checks[userID+"/"+name]
	return c, ok
}

// ForgetCheck drops a script's last check (after its settings changed).
func (r *Runner) ForgetCheck(userID, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.checks, userID+"/"+name)
}

// redact replaces every secret value with «NAME» (§4.15.5), longest first so
// a secret containing another is replaced whole.
func redact(s string, secrets map[string]string) string {
	type kv struct{ name, value string }
	var list []kv
	for n, v := range secrets {
		if len(v) >= 4 {
			list = append(list, kv{n, v})
		}
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i].value) > len(list[j].value) })
	for _, x := range list {
		s = strings.ReplaceAll(s, x.value, "«"+x.name+"»")
	}
	return s
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// capped is a writer that keeps at most max bytes and notes the overflow.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.over = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.over = true
	}
	return len(p), nil
}

func (c *capped) String() string { return c.buf.String() }

// ErrNoPython is what the Scripts page shows when the server has no python3.
var ErrNoPython = errors.New("python3 is not installed on the sessile server")
