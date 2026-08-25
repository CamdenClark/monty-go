package monty

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// PrintStream identifies stdout or stderr.
type PrintStream uint32

const (
	Stdout PrintStream = 1
	Stderr PrintStream = 2
)

func (s PrintStream) String() string {
	if s == Stderr {
		return "stderr"
	}
	return "stdout"
}

// PrintCallback receives line-buffered sandbox output.
type PrintCallback func(PrintStream, string) error

// FeedOptions configures one snippet.
type FeedOptions struct {
	Inputs         map[string]Value
	ExternalLookup ExternalLookup
	OS             OSHandler
	PrintCallback  PrintCallback
	SkipTypeCheck  bool
}

// Session is a persistent Monty REPL checked out from a pool.
type Session struct {
	pool             *Monty
	worker           *worker
	scriptName       string
	mu               sync.Mutex
	closed           bool
	used             bool
	inTurn           bool
	pending          *runState
	maxDuration      time.Duration
	execution        time.Duration
	durationGrace    time.Duration
	durationBackstop bool
}

// ScriptName returns the diagnostic filename for this session.
func (s *Session) ScriptName() string { s.mu.Lock(); defer s.mu.Unlock(); return s.scriptName }

// executionExchange applies the cumulative MaxDuration watchdog only while a
// request can execute sandbox bytecode. Host callback wait time is outside it.
func (s *Session) executionExchange(ctx context.Context, req request, print PrintCallback) (childEvent, error) {
	if s.durationBackstop && s.maxDuration > 0 {
		remaining := s.maxDuration - s.execution + s.durationGrace
		if remaining <= 0 {
			return childEvent{}, &CrashedError{Message: "Monty execution duration limit exhausted", TimedOut: true}
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, remaining)
		defer cancel()
	}
	e, err := s.worker.exchange(ctx, req, print)
	if err == nil {
		s.observeEvent(e)
	}
	return e, err
}
func (s *Session) observeEvent(e childEvent) {
	s.execution = time.Duration(e.totalExecutionMicros) * time.Microsecond
	if e.maxDurationMicros != nil {
		s.maxDuration = time.Duration(*e.maxDurationMicros) * time.Microsecond
	}
}

// Close resets the worker and returns it to the pool. It is idempotent.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if s.inTurn {
		s.mu.Unlock()
		return errors.New("cannot close a Monty session during an active protocol turn")
	}
	s.closed = true
	s.pending = nil
	s.mu.Unlock()
	ctx := context.Background()
	e, err := s.worker.exchange(ctx, request{reqReset, nil}, nil)
	reusable := err == nil && e.kind == eventOK
	s.pool.release(s.worker, reusable)
	if err != nil {
		return err
	}
	if !reusable {
		return eventErrorValue(e)
	}
	return nil
}

// Complete is a successfully finished feed.
type Complete struct{ Output Value }

// Progress is either *Complete or a resumable snapshot.
type Progress interface{ isProgress() }

func (*Complete) isProgress() {}

// Snapshot is a suspended feed which can be dumped or automatically resumed.
type Snapshot interface {
	Progress
	ResumeAuto(context.Context) (Progress, error)
	Dump(context.Context) ([]byte, error)
}

type runState struct {
	session *Session
	options FeedOptions
	futures map[uint32]<-chan futureOutcome
	step    uint64
}

func newRunState(session *Session, options FeedOptions) *runState {
	return &runState{session: session, options: options, futures: make(map[uint32]<-chan futureOutcome)}
}

type futureOutcome struct {
	value Value
	err   error
}

// FunctionSnapshot is an external or OS call suspension.
type FunctionSnapshot struct {
	state        *runState
	step         uint64
	used         atomic.Bool
	FunctionName string
	Args         []Value
	Kwargs       Dict
	CallID       uint32
	MethodCall   bool
	IsOSFunction bool
}

func (*FunctionSnapshot) isProgress() {}

// NameLookupSnapshot is an unresolved global-name suspension.
type NameLookupSnapshot struct {
	state *runState
	step  uint64
	used  atomic.Bool
	Name  string
}

func (*NameLookupSnapshot) isProgress() {}

// FutureSnapshot means all sandbox tasks are waiting for host futures.
type FutureSnapshot struct {
	state          *runState
	step           uint64
	used           atomic.Bool
	PendingCallIDs []uint32
}

func (*FutureSnapshot) isProgress() {}
func (s *FutureSnapshot) claim() error {
	if !s.used.CompareAndSwap(false, true) {
		return errors.New("snapshot has already been resumed")
	}
	return nil
}

// FeedStart begins a snippet and returns at its first suspension or completion.
func (s *Session) FeedStart(ctx context.Context, code string, options ...FeedOptions) (Progress, error) {
	var o FeedOptions
	if len(options) > 1 {
		return nil, fmt.Errorf("FeedStart accepts at most one FeedOptions value")
	}
	if len(options) == 1 {
		o = options[0]
	}
	req, err := feedRequest(code, o.Inputs, o.SkipTypeCheck)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("Monty session is closed")
	}
	if s.pending != nil {
		s.mu.Unlock()
		return nil, errors.New("Monty session has a suspended feed; resume it before starting another")
	}
	if s.inTurn {
		s.mu.Unlock()
		return nil, errors.New("Monty session already has an active protocol turn")
	}
	s.used = true
	s.inTurn = true
	s.mu.Unlock()
	state := newRunState(s, o)
	e, err := s.executionExchange(ctx, req, o.PrintCallback)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inTurn = false
	if err != nil {
		return nil, err
	}
	p, err := state.progress(e)
	if err != nil {
		return nil, err
	}
	if _, ok := p.(*Complete); !ok {
		s.pending = state
	}
	return p, nil
}

// FeedRun drives a snippet and all configured host callbacks to completion.
func (s *Session) FeedRun(ctx context.Context, code string, options ...FeedOptions) (Value, error) {
	p, err := s.FeedStart(ctx, code, options...)
	if err != nil {
		return nil, err
	}
	for {
		if done, ok := p.(*Complete); ok {
			return done.Output, nil
		}
		snapshot, ok := p.(Snapshot)
		if !ok {
			return nil, &ProtocolError{Message: fmt.Sprintf("unknown progress type %T", p)}
		}
		p, err = snapshot.ResumeAuto(ctx)
		if err != nil {
			return nil, err
		}
	}
}

func (r *runState) progress(e childEvent) (Progress, error) {
	switch e.kind {
	case eventComplete:
		v, err := decodeComplete(e.body)
		if err != nil {
			return nil, &ProtocolError{err.Error()}
		}
		r.session.pending = nil
		return &Complete{v}, nil
	case eventFunctionCall:
		c, err := decodeFunctionCall(e.body)
		if err != nil {
			return nil, &ProtocolError{err.Error()}
		}
		return &FunctionSnapshot{state: r, step: r.step, FunctionName: c.Name, Args: c.Args, Kwargs: c.Kwargs, CallID: c.CallID, MethodCall: c.MethodCall}, nil
	case eventOSCall:
		c, err := decodeOSCall(e.body)
		if err != nil {
			return nil, &ProtocolError{err.Error()}
		}
		return &FunctionSnapshot{state: r, step: r.step, FunctionName: c.Name, Args: c.Args, Kwargs: c.Kwargs, CallID: c.CallID, IsOSFunction: true}, nil
	case eventNameLookup:
		return &NameLookupSnapshot{state: r, step: r.step, Name: decodeStringField(e.body, 1)}, nil
	case eventResolveFutures:
		return &FutureSnapshot{state: r, step: r.step, PendingCallIDs: decodeUint32List(e.body, 1)}, nil
	case eventError:
		x, err := decodeErrorEvent(e.body)
		r.session.pending = nil
		if err != nil {
			return nil, &ProtocolError{err.Error()}
		}
		return nil, classifyRuntime(x)
	case eventTypingError:
		r.session.pending = nil
		return nil, &TypingError{Diagnostics: decodeStringField(e.body, 1)}
	case eventFatal:
		r.session.pending = nil
		return nil, &ProtocolError{Message: decodeStringField(e.body, 1)}
	default:
		return nil, &ProtocolError{Message: fmt.Sprintf("unexpected event kind %d during feed", e.kind)}
	}
}

func (r *runState) exchange(ctx context.Context, req request, step uint64) (Progress, error) {
	s := r.session
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("Monty session is closed")
	}
	if s.pending != r {
		s.mu.Unlock()
		return nil, errors.New("snapshot no longer belongs to the session's active feed")
	}
	if s.inTurn {
		s.mu.Unlock()
		return nil, errors.New("Monty session already has an active protocol turn")
	}
	if r.step != step {
		s.mu.Unlock()
		return nil, errors.New("snapshot has already been resumed")
	}
	r.step++
	s.inTurn = true
	s.mu.Unlock()
	e, err := s.executionExchange(ctx, req, r.options.PrintCallback)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inTurn = false
	if err != nil {
		s.pending = nil
		return nil, err
	}
	return r.progress(e)
}

// Resume returns a value from a suspended function call.
func (s *FunctionSnapshot) Resume(ctx context.Context, value Value) (Progress, error) {
	if err := s.claim(); err != nil {
		return nil, err
	}
	return s.resume(ctx, resumeResult{value: value})
}
func (s *FunctionSnapshot) claim() error {
	if !s.used.CompareAndSwap(false, true) {
		return errors.New("snapshot has already been resumed")
	}
	return nil
}
func (s *FunctionSnapshot) resume(ctx context.Context, result resumeResult) (Progress, error) {
	req, err := resumeCallRequest(s.CallID, result)
	if err != nil {
		// A host callback produced a value which cannot cross the Monty
		// boundary. Raise it inside Python instead of abandoning the suspended
		// feed with a host-side codec error.
		raised := RaisedException{Type: "RuntimeError", Message: err.Error()}
		req, err = resumeCallRequest(s.CallID, resumeResult{err: &raised})
		if err != nil {
			return nil, err
		}
	}
	return s.state.exchange(ctx, req, s.step)
}

// ResumeError raises a chosen Python exception at a suspended call.
func (s *FunctionSnapshot) ResumeError(ctx context.Context, err error) (Progress, error) {
	if claimErr := s.claim(); claimErr != nil {
		return nil, claimErr
	}
	return s.resumeError(ctx, err)
}
func (s *FunctionSnapshot) resumeError(ctx context.Context, err error) (Progress, error) {
	x := raisedFromError(err)
	return s.resume(ctx, resumeResult{err: &x})
}

// ResumeNotHandled asks Monty to apply an OS call's default no-handler behavior.
func (s *FunctionSnapshot) ResumeNotHandled(ctx context.Context) (Progress, error) {
	if !s.IsOSFunction {
		return nil, errors.New("NotHandled is only valid for OS calls")
	}
	if err := s.claim(); err != nil {
		return nil, err
	}
	return s.resume(ctx, resumeResult{notHandled: true})
}

// ResumeNotFound raises NameError for a missing external function.
func (s *FunctionSnapshot) ResumeNotFound(ctx context.Context) (Progress, error) {
	if err := s.claim(); err != nil {
		return nil, err
	}
	name := s.FunctionName
	return s.resume(ctx, resumeResult{notFound: &name})
}

// ResumeFuture registers this call as an external future. Resolve it from the
// resulting FutureSnapshot with FutureSnapshot.Resume.
func (s *FunctionSnapshot) ResumeFuture(ctx context.Context) (Progress, error) {
	if err := s.claim(); err != nil {
		return nil, err
	}
	id := s.CallID
	return s.resume(ctx, resumeResult{future: &id})
}
func (s *FunctionSnapshot) ResumeAuto(ctx context.Context) (Progress, error) {
	if err := s.claim(); err != nil {
		return nil, err
	}
	if s.IsOSFunction {
		handler := s.state.options.OS
		if handler == nil {
			return s.resume(ctx, resumeResult{notHandled: true})
		}
		value, err := handler(ctx, s.FunctionName, s.Args, mustKwargs(s.Kwargs))
		if err != nil {
			return s.resumeError(ctx, err)
		}
		if _, ok := value.(notHandled); ok {
			return s.resume(ctx, resumeResult{notHandled: true})
		}
		return s.resume(ctx, resumeResult{value: value})
	}
	if s.MethodCall {
		return s.resumeError(ctx, &HostError{Type: "RuntimeError", Message: "method calls on host objects are not supported: " + s.FunctionName})
	}
	fn, ok := s.state.options.ExternalLookup[s.FunctionName]
	if !ok || !isHostFunction(fn) {
		return s.resumeError(ctx, &HostError{Type: "NameError", Message: "external function not found: " + s.FunctionName})
	}
	if _, async := fn.(AsyncFunction); !async {
		value, err := callHost(ctx, fn, s.Args, s.Kwargs)
		if err != nil {
			return s.resumeError(ctx, err)
		}
		return s.resume(ctx, resumeResult{value: value})
	}
	ch := make(chan futureOutcome, 1)
	go func() { v, e := callHost(ctx, fn, s.Args, s.Kwargs); ch <- futureOutcome{v, e} }()
	s.state.futures[s.CallID] = ch
	id := s.CallID
	return s.resume(ctx, resumeResult{future: &id})
}
func mustKwargs(d Dict) Kwargs { k, _ := kwargsMap(d); return k }

// Resume supplies the value for an unresolved name.
func (s *NameLookupSnapshot) Resume(ctx context.Context, value Value) (Progress, error) {
	if err := s.claim(); err != nil {
		return nil, err
	}
	return s.resume(ctx, value)
}
func (s *NameLookupSnapshot) claim() error {
	if !s.used.CompareAndSwap(false, true) {
		return errors.New("snapshot has already been resumed")
	}
	return nil
}
func (s *NameLookupSnapshot) resume(ctx context.Context, value Value) (Progress, error) {
	req, err := resumeNameValueRequest(value)
	if err != nil {
		return nil, err
	}
	return s.state.exchange(ctx, req, s.step)
}

// ResumeUndefined answers an unresolved name with Python NameError.
func (s *NameLookupSnapshot) ResumeUndefined(ctx context.Context) (Progress, error) {
	if err := s.claim(); err != nil {
		return nil, err
	}
	return s.state.exchange(ctx, resumeNameUndefinedRequest(), s.step)
}
func (s *NameLookupSnapshot) ResumeAuto(ctx context.Context) (Progress, error) {
	if err := s.claim(); err != nil {
		return nil, err
	}
	value, ok := s.state.options.ExternalLookup[s.Name]
	if !ok {
		return s.state.exchange(ctx, resumeNameUndefinedRequest(), s.step)
	}
	if isHostFunction(value) {
		doc := functionDoc(value)
		return s.resume(ctx, Function{Name: s.Name, Docstring: doc})
	}
	return s.resume(ctx, value)
}
func functionDoc(value any) *string {
	type documenter interface{ Docstring() string }
	if d, ok := value.(documenter); ok {
		x := d.Docstring()
		return &x
	}
	return nil
}

func (s *FutureSnapshot) ResumeAuto(ctx context.Context) (Progress, error) {
	if err := s.claim(); err != nil {
		return nil, err
	}
	results := make([]futureWireResult, 0, len(s.PendingCallIDs))
	for _, id := range s.PendingCallIDs {
		ch, ok := s.state.futures[id]
		if !ok {
			return nil, &ProtocolError{Message: fmt.Sprintf("worker requested unknown future %d", id)}
		}
		select {
		case outcome := <-ch:
			delete(s.state.futures, id)
			r := resumeResult{value: outcome.value}
			if outcome.err != nil {
				x := raisedFromError(outcome.err)
				r.err = &x
			}
			results = append(results, futureWireResult{id, r})
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	req, err := resumeFuturesRequest(results)
	if err != nil {
		return nil, err
	}
	return s.state.exchange(ctx, req, s.step)
}

// Resume resolves selected pending futures manually.
func (s *FutureSnapshot) Resume(ctx context.Context, results map[uint32]Value) (Progress, error) {
	if err := s.claim(); err != nil {
		return nil, err
	}
	wire := make([]futureWireResult, 0, len(results))
	for id, value := range results {
		wire = append(wire, futureWireResult{id, resumeResult{value: value}})
	}
	req, err := resumeFuturesRequest(wire)
	if err != nil {
		return nil, err
	}
	return s.state.exchange(ctx, req, s.step)
}

func snapshotDump(ctx context.Context, state *runState, step uint64) ([]byte, error) {
	session := state.session
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil, errors.New("Monty session is closed")
	}
	if session.pending != state {
		return nil, errors.New("snapshot is no longer active")
	}
	if state.step != step {
		return nil, errors.New("snapshot has already been resumed")
	}
	e, err := session.worker.exchange(ctx, request{reqDump, nil}, state.options.PrintCallback)
	if err != nil {
		return nil, err
	}
	if e.kind != eventDump {
		return nil, eventErrorValue(e)
	}
	return decodeBytesField(e.body, 1), nil
}
func (s *FunctionSnapshot) Dump(ctx context.Context) ([]byte, error) {
	if s.used.Load() {
		return nil, errors.New("snapshot has already been resumed")
	}
	return snapshotDump(ctx, s.state, s.step)
}
func (s *NameLookupSnapshot) Dump(ctx context.Context) ([]byte, error) {
	if s.used.Load() {
		return nil, errors.New("snapshot has already been resumed")
	}
	return snapshotDump(ctx, s.state, s.step)
}
func (s *FutureSnapshot) Dump(ctx context.Context) ([]byte, error) {
	if s.used.Load() {
		return nil, errors.New("snapshot has already been resumed")
	}
	return snapshotDump(ctx, s.state, s.step)
}

// Dump serializes an idle session between feeds.
func (s *Session) Dump(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("Monty session is closed")
	}
	if s.pending != nil {
		return nil, errors.New("session has a suspended feed; call Snapshot.Dump instead")
	}
	if s.inTurn {
		return nil, errors.New("Monty session already has an active protocol turn")
	}
	e, err := s.worker.exchange(ctx, request{reqDump, nil}, nil)
	if err != nil {
		return nil, err
	}
	if e.kind != eventDump {
		return nil, eventErrorValue(e)
	}
	return decodeBytesField(e.body, 1), nil
}

// InstallDependencies asks a compatible embedded-CPython worker to install
// PEP 508 requirements. The standard Monty sandbox worker returns an error.
func (s *Session) InstallDependencies(ctx context.Context, requirements ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("Monty session is closed")
	}
	if s.pending != nil {
		return errors.New("cannot install dependencies while a feed is suspended")
	}
	if s.inTurn {
		return errors.New("Monty session already has an active protocol turn")
	}
	body := []byte{}
	for _, requirement := range requirements {
		body = append(body, fieldString(1, requirement)...)
	}
	e, err := s.worker.exchange(ctx, request{reqInstall, body}, nil)
	if err != nil {
		return err
	}
	if e.kind != eventOK {
		return eventErrorValue(e)
	}
	return nil
}

// WorkerPID returns the local worker process ID, or zero after it exits.
func (s *Session) WorkerPID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker == nil || s.worker.cmd == nil || s.worker.cmd.Process == nil {
		return 0
	}
	return s.worker.cmd.Process.Pid
}

// LoadSession restores an idle dump into a fresh session.
func (s *Session) LoadSession(ctx context.Context, state []byte) error {
	p, err := s.load(ctx, state, FeedOptions{})
	if err != nil {
		return err
	}
	if p != nil {
		return errors.New("dump contains a suspended feed; use LoadSnapshot")
	}
	return nil
}

// LoadSnapshot restores a suspended dump into a fresh session.
func (s *Session) LoadSnapshot(ctx context.Context, state []byte, options ...FeedOptions) (Snapshot, error) {
	var o FeedOptions
	if len(options) > 1 {
		return nil, fmt.Errorf("LoadSnapshot accepts at most one FeedOptions value")
	}
	if len(options) == 1 {
		o = options[0]
	}
	p, err := s.load(ctx, state, o)
	if err != nil {
		return nil, err
	}
	snap, ok := p.(Snapshot)
	if !ok {
		return nil, errors.New("dump contains an idle session; use LoadSession")
	}
	return snap, nil
}
func (s *Session) load(ctx context.Context, state []byte, o FeedOptions) (Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("Monty session is closed")
	}
	if s.pending != nil {
		return nil, errors.New("session already has state")
	}
	if s.inTurn {
		return nil, errors.New("Monty session already has an active protocol turn")
	}
	if s.used {
		return nil, errors.New("LoadSession and LoadSnapshot require a fresh session")
	}
	s.used = true
	r := newRunState(s, o)
	e, err := s.worker.exchange(ctx, request{reqLoad, fieldBytes(1, state)}, o.PrintCallback)
	if err != nil {
		return nil, err
	}
	if e.restoredScriptName != nil {
		s.scriptName = *e.restoredScriptName
	}
	s.observeEvent(e)
	if e.kind == eventOK {
		return nil, nil
	}
	p, err := r.progress(e)
	if err != nil {
		return nil, err
	}
	if _, ok := p.(*Complete); ok {
		return nil, errors.New("invalid completed load event")
	}
	s.pending = r
	return p, nil
}
