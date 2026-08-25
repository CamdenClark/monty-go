package monty

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Version is the version of this Go wrapper.
const Version = "0.2.0"

// Options configures a Monty worker pool.
type Options struct {
	BinaryPath string
	// CacheDir overrides MONTY_CACHE_DIR and the operating system's user cache
	// when resolving or automatically installing a Monty runtime.
	CacheDir string
	// AutoInstall downloads the pinned runtime when no existing binary resolves.
	AutoInstall     bool
	MinProcesses    int
	MaxProcesses    int
	CheckoutTimeout time.Duration
	RequestTimeout  time.Duration
	// DurationLimitGrace is the watchdog grace beyond MaxDuration. Zero uses
	// one second. Set DisableDurationLimitBackstop to rely only on in-sandbox checks.
	DurationLimitGrace           time.Duration
	DisableDurationLimitBackstop bool
	MaxCheckoutsPerWorker        int
}

// ResourceLimits apply to an entire session. Zero means unlimited, except
// Monty's own default recursion limit still applies.
type ResourceLimits struct {
	MaxDuration       time.Duration
	MaxMemory         uint64
	GCInterval        uint64
	MaxRecursionDepth uint64
}

// TypeCheckFormat selects ty's diagnostic rendering.
type TypeCheckFormat string

const (
	TypeCheckFull      TypeCheckFormat = "full"
	TypeCheckConcise   TypeCheckFormat = "concise"
	TypeCheckAzure     TypeCheckFormat = "azure"
	TypeCheckJSON      TypeCheckFormat = "json"
	TypeCheckJSONLines TypeCheckFormat = "jsonlines"
	TypeCheckRDJSON    TypeCheckFormat = "rdjson"
	TypeCheckPylint    TypeCheckFormat = "pylint"
	TypeCheckGitLab    TypeCheckFormat = "gitlab"
	TypeCheckGitHub    TypeCheckFormat = "github"
)

func (f TypeCheckFormat) wire() uint64 {
	switch f {
	case "", TypeCheckFull:
		return 1
	case TypeCheckConcise:
		return 2
	case TypeCheckAzure:
		return 3
	case TypeCheckJSON:
		return 4
	case TypeCheckJSONLines:
		return 5
	case TypeCheckRDJSON:
		return 6
	case TypeCheckPylint:
		return 7
	case TypeCheckGitLab:
		return 8
	case TypeCheckGitHub:
		return 9
	default:
		return 0
	}
}

// CheckoutOptions configures one persistent REPL session.
type CheckoutOptions struct {
	ScriptName               string
	Limits                   ResourceLimits
	TypeCheck                bool
	TypeCheckStubs           string
	TypeCheckFormat          TypeCheckFormat
	TypeCheckColor           bool
	AssertMessageAnnotations any // nil/true = default, false = disabled, positive uint32 = truncation bytes
}

// Monty owns a pool of crash-isolated subprocess workers.
type Monty struct {
	options Options
	binary  string
	idle    chan *worker
	slots   chan struct{}
	mu      sync.Mutex
	closed  bool
	workers map[*worker]struct{}
}

// New creates and prewarms a Monty pool. Options may be omitted.
func New(ctx context.Context, options ...Options) (*Monty, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create Monty pool: %w", err)
	}
	var o Options
	if len(options) > 1 {
		return nil, fmt.Errorf("New accepts at most one Options value")
	}
	if len(options) == 1 {
		o = options[0]
	}
	if o.MaxProcesses == 0 {
		o.MaxProcesses = runtime.GOMAXPROCS(0)
	}
	if o.MaxProcesses < 1 {
		return nil, fmt.Errorf("MaxProcesses must be at least 1")
	}
	if o.MinProcesses == 0 {
		o.MinProcesses = 1
	}
	if o.MinProcesses < 0 || o.MinProcesses > o.MaxProcesses {
		return nil, fmt.Errorf("MinProcesses must be between 0 and MaxProcesses")
	}
	if o.MaxCheckoutsPerWorker < 0 {
		return nil, fmt.Errorf("MaxCheckoutsPerWorker cannot be negative")
	}
	if o.DurationLimitGrace < 0 {
		return nil, fmt.Errorf("DurationLimitGrace cannot be negative")
	}
	binary, err := findBinary(o.BinaryPath, o.CacheDir)
	if err != nil && o.AutoInstall && o.BinaryPath == "" {
		binary, err = Install(ctx, InstallOptions{CacheDir: o.CacheDir})
	}
	if err != nil {
		return nil, err
	}
	p := &Monty{options: o, binary: binary, idle: make(chan *worker, o.MaxProcesses), slots: make(chan struct{}, o.MaxProcesses), workers: make(map[*worker]struct{})}
	for range o.MinProcesses {
		if err := ctx.Err(); err != nil {
			_ = p.Close()
			return nil, fmt.Errorf("prewarm Monty pool: %w", err)
		}
		w, err := p.startWorker()
		if err != nil {
			_ = p.Close()
			return nil, err
		}
		p.idle <- w
	}
	return p, nil
}

// Create is an alias for New, matching the TypeScript wrapper's factory name.
func Create(ctx context.Context, options ...Options) (*Monty, error) { return New(ctx, options...) }

// FindBinary resolves a Monty executable: explicit path, MONTY_BIN, PATH, the
// versioned runtime cache, an installed npm platform package, then a nearby
// Cargo target directory.
func FindBinary(explicit string) (string, error) {
	return findBinary(explicit, "")
}

func findBinary(explicit, cacheDir string) (string, error) {
	if explicit != "" {
		return executableFile(explicit, "BinaryPath")
	}
	if env := os.Getenv("MONTY_BIN"); env != "" {
		if p, e := executableFile(env, "MONTY_BIN"); e == nil {
			return p, nil
		}
	}
	if p, e := exec.LookPath("monty"); e == nil {
		return p, nil
	}
	var cacheErr error
	if artifact, e := currentRuntimeArtifact(); e == nil {
		if p, e := cachedRuntimeBinary(cacheDir, artifact); e == nil {
			return p, nil
		} else {
			cacheErr = e
		}
	}
	triple := ""
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/amd64":
		triple = "darwin-x64"
	case "darwin/arm64":
		triple = "darwin-arm64"
	case "linux/amd64":
		triple = "linux-x64-gnu"
	case "linux/arm64":
		triple = "linux-arm64-gnu"
	case "windows/amd64":
		triple = "win32-x64-msvc"
	}
	exe := "monty"
	if runtime.GOOS == "windows" {
		exe = "monty.exe"
	}
	if cwd, e := os.Getwd(); e == nil {
		for dir := cwd; ; dir = filepath.Dir(dir) {
			if triple != "" {
				candidate := filepath.Join(dir, "node_modules", "@pydantic", "monty-"+triple, exe)
				if p, e := executableFile(candidate, "npm package"); e == nil {
					return p, nil
				}
			}
			for _, profile := range []string{"debug", "release"} {
				candidate := filepath.Join(dir, "target", profile, exe)
				if p, e := executableFile(candidate, "Cargo target"); e == nil {
					return p, nil
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}
	message := "could not locate the monty binary (call monty.Install, enable Options.AutoInstall, pass BinaryPath, or set MONTY_BIN)"
	if cacheErr != nil && !errors.Is(cacheErr, os.ErrNotExist) {
		return "", fmt.Errorf("%s: %w", message, cacheErr)
	}
	return "", errors.New(message)
}
func executableFile(path, source string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("monty binary from %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("monty binary from %s is not a regular file: %s", source, path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("monty binary from %s is not executable: %s", source, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func (p *Monty) startWorker() (*worker, error) {
	w, err := newWorker(p.binary, p.options.RequestTimeout)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		w.kill()
		return nil, errors.New("Monty pool is closed")
	}
	p.workers[w] = struct{}{}
	return w, nil
}

// Checkout gets a worker and creates a stateful REPL session.
func (p *Monty) Checkout(ctx context.Context, options ...CheckoutOptions) (*Session, error) {
	var o CheckoutOptions
	if len(options) > 1 {
		return nil, fmt.Errorf("Checkout accepts at most one CheckoutOptions value")
	}
	if len(options) == 1 {
		o = options[0]
	}
	if o.ScriptName == "" {
		o.ScriptName = "main.py"
	}
	if o.TypeCheckFormat.wire() == 0 {
		return nil, fmt.Errorf("unknown TypeCheckFormat %q", o.TypeCheckFormat)
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, errors.New("Monty pool is closed")
	}
	waitCtx := ctx
	var cancel context.CancelFunc
	if p.options.CheckoutTimeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, p.options.CheckoutTimeout)
		defer cancel()
	}
	select {
	case p.slots <- struct{}{}:
	case <-waitCtx.Done():
		return nil, fmt.Errorf("checkout Monty worker: %w", waitCtx.Err())
	}
	var w *worker
	select {
	case w = <-p.idle:
	default:
		var err error
		w, err = p.startWorker()
		if err != nil {
			<-p.slots
			return nil, err
		}
	}
	annotations, err := normalizeAnnotations(o.AssertMessageAnnotations)
	if err != nil {
		p.release(w, false)
		return nil, err
	}
	var stubs *string
	if o.TypeCheckStubs != "" {
		stubs = &o.TypeCheckStubs
	}
	e, err := w.exchange(ctx, configureRequest(configureWire{o.ScriptName, o.Limits, o.TypeCheck, stubs, o.TypeCheckFormat, o.TypeCheckColor, annotations}), nil)
	if err != nil {
		p.release(w, false)
		return nil, err
	}
	if e.kind != eventOK {
		p.release(w, false)
		return nil, eventErrorValue(e)
	}
	w.checkouts++
	grace := p.options.DurationLimitGrace
	if grace == 0 {
		grace = time.Second
	}
	return &Session{pool: p, worker: w, scriptName: o.ScriptName, maxDuration: o.Limits.MaxDuration, durationGrace: grace, durationBackstop: !p.options.DisableDurationLimitBackstop}, nil
}
func normalizeAnnotations(x any) (*uint32, error) {
	if x == nil {
		return nil, nil
	}
	switch v := x.(type) {
	case bool:
		if v {
			return nil, nil
		}
		z := uint32(0)
		return &z, nil
	case int:
		if v < 1 || uint64(v) > math.MaxUint32 {
			return nil, fmt.Errorf("AssertMessageAnnotations must be true, false, or 1..2^32-1")
		}
		z := uint32(v)
		return &z, nil
	case uint32:
		z := v
		if z == 0 {
			return nil, fmt.Errorf("AssertMessageAnnotations integer must be positive")
		}
		return &z, nil
	default:
		return nil, fmt.Errorf("AssertMessageAnnotations has unsupported type %T", x)
	}
}

func (p *Monty) release(w *worker, reusable bool) {
	<-p.slots
	p.mu.Lock()
	closed := p.closed
	_, known := p.workers[w]
	if !reusable || closed || (p.options.MaxCheckoutsPerWorker > 0 && w.checkouts >= p.options.MaxCheckoutsPerWorker) {
		delete(p.workers, w)
		known = false
	}
	p.mu.Unlock()
	if !known {
		w.kill()
		return
	}
	select {
	case p.idle <- w:
	default:
		p.mu.Lock()
		delete(p.workers, w)
		p.mu.Unlock()
		w.kill()
	}
}

// Close prevents new checkouts and shuts down idle workers. Checked-out
// sessions retain their workers until Session.Close.
func (p *Monty) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	for {
		select {
		case w := <-p.idle:
			p.mu.Lock()
			delete(p.workers, w)
			p.mu.Unlock()
			_ = w.shutdown()
		default:
			return nil
		}
	}
}

type worker struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderrMu  sync.Mutex
	stderr    bytes.Buffer
	mu        sync.Mutex
	timeout   time.Duration
	checkouts int
	dead      bool
}

func newWorker(binary string, timeout time.Duration) (*worker, error) {
	cmd := exec.Command(binary, "subprocess")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	w := &worker{cmd: cmd, stdin: stdin, stdout: stdout, timeout: timeout}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Monty worker: %w", err)
	}
	go w.captureStderr(stderr)
	return w, nil
}
func (w *worker) captureStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, e := r.Read(buf)
		if n > 0 {
			w.stderrMu.Lock()
			if w.stderr.Len()+n > 64<<10 {
				old := w.stderr.Bytes()
				keep := 32 << 10
				if len(old) > keep {
					old = old[len(old)-keep:]
				}
				w.stderr.Reset()
				_, _ = w.stderr.Write(old)
			}
			_, _ = w.stderr.Write(buf[:n])
			w.stderrMu.Unlock()
		}
		if e != nil {
			return
		}
	}
}
func (w *worker) stderrText() string {
	w.stderrMu.Lock()
	defer w.stderrMu.Unlock()
	return w.stderr.String()
}
func (w *worker) exchange(ctx context.Context, req request, onPrint PrintCallback) (childEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead {
		return childEvent{}, &CrashedError{Message: "Monty worker is no longer running"}
	}
	if w.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, w.timeout)
		defer cancel()
	}
	type answer struct {
		event       childEvent
		err         error
		callbackErr bool
	}
	done := make(chan answer, 1)
	go func() {
		if err := writeFrame(w.stdin, req.encode()); err != nil {
			done <- answer{err: err}
			return
		}
		var callbackErr error
		for {
			frame, err := readFrame(w.stdout)
			if err != nil {
				done <- answer{err: err}
				return
			}
			event, err := decodeEvent(frame)
			if err != nil {
				done <- answer{err: err}
				return
			}
			if event.kind == eventPrint {
				if callbackErr == nil {
					p := decodePrint(event.body)
					if onPrint != nil {
						callbackErr = onPrint(p.Stream, p.Text)
					} else {
						target := os.Stdout
						if p.Stream == Stderr {
							target = os.Stderr
						}
						_, callbackErr = io.WriteString(target, p.Text)
					}
				}
				continue
			}
			if callbackErr != nil {
				done <- answer{err: callbackErr, callbackErr: true}
				return
			}
			done <- answer{event: event}
			return
		}
	}()
	select {
	case a := <-done:
		if a.err != nil {
			if a.callbackErr {
				return childEvent{}, a.err
			}
			w.dead = true
			w.killUnlocked()
			stderr := w.stderrText()
			if stderr != "" {
				return childEvent{}, &CrashedError{Message: fmt.Sprintf("Monty worker failed: %v\n%s", a.err, stderr), ExitStatus: w.exitStatus()}
			}
			return childEvent{}, &CrashedError{Message: fmt.Sprintf("Monty worker failed: %v", a.err), ExitStatus: w.exitStatus()}
		}
		if a.event.kind == eventFatal {
			w.dead = true
			w.killUnlocked()
		}
		return a.event, nil
	case <-ctx.Done():
		w.dead = true
		w.killUnlocked()
		return childEvent{}, &CrashedError{Message: "Monty worker request timed out: " + ctx.Err().Error(), TimedOut: true, ExitStatus: w.exitStatus()}
	}
}
func (w *worker) kill() { w.mu.Lock(); defer w.mu.Unlock(); w.killUnlocked() }
func (w *worker) killUnlocked() {
	w.dead = true
	if w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
	}
	_ = w.stdin.Close()
	_ = w.stdout.Close()
	_ = w.cmd.Wait()
}
func (w *worker) exitStatus() string {
	if w.cmd != nil && w.cmd.ProcessState != nil {
		return w.cmd.ProcessState.String()
	}
	return ""
}
func (w *worker) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e, err := w.exchange(ctx, request{reqShutdown, nil}, nil)
	if err == nil && e.kind != eventOK {
		err = eventErrorValue(e)
	}
	w.kill()
	return err
}

func eventErrorValue(e childEvent) error {
	switch e.kind {
	case eventError:
		x, err := decodeErrorEvent(e.body)
		if err != nil {
			return &ProtocolError{err.Error()}
		}
		return classifyRuntime(x)
	case eventTypingError:
		return &TypingError{Diagnostics: decodeStringField(e.body, 1)}
	case eventFatal:
		return &ProtocolError{Message: decodeStringField(e.body, 1)}
	default:
		return &ProtocolError{Message: fmt.Sprintf("unexpected event kind %d", e.kind)}
	}
}
