package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

var ErrAlreadyRunning = errors.New("notification monitor is already running")

const (
	maxClients           = 16
	maxPending           = 128
	maxReplayIDs         = 1024
	clientDeadline       = 2 * time.Second
	defaultCoalesceDelay = 175 * time.Millisecond
)

type Server struct {
	Home          string
	Forwarder     Forwarder
	Logger        *log.Logger
	CoalesceDelay time.Duration

	mu           sync.Mutex
	generation   string
	listener     *net.UnixListener
	shutdown     chan struct{}
	shutdownOnce sync.Once
	pending      map[string]*pendingEvent
	replays      map[string]struct{}
	replayOrder  []string
	active       chan struct{}
	rateStart    time.Time
	rateCount    int
	workers      sync.WaitGroup
	timers       sync.WaitGroup
}

type pendingEvent struct {
	event Event
	timer *time.Timer
}

func (s *Server) Run(ctx context.Context) error {
	if s.Forwarder == nil {
		return errors.New("monitor forwarder is required")
	}
	if s.Logger == nil {
		s.Logger = log.New(io.Discard, "", 0)
	}
	if s.CoalesceDelay <= 0 {
		s.CoalesceDelay = defaultCoalesceDelay
	}
	if err := EnsureRuntimeDir(s.Home); err != nil {
		return err
	}
	s.shutdown = make(chan struct{})
	s.pending = make(map[string]*pendingEvent)
	s.replays = make(map[string]struct{})
	s.active = make(chan struct{}, maxClients)
	if err := s.bind(); err != nil {
		return err
	}
	s.Logger.Printf("monitor ready protocol=%d pid=%d", ProtocolVersion, os.Getpid())
	defer func() {
		s.initiateShutdown()
		s.workers.Wait()
		s.flushPending()
		s.timers.Wait()
		s.cleanup()
		s.Logger.Printf("monitor stopped")
	}()

	go func() {
		select {
		case <-ctx.Done():
			s.initiateShutdown()
		case <-s.shutdown:
		}
	}()

	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			select {
			case <-s.shutdown:
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			s.Logger.Printf("accept failed: %v", err)
			continue
		}
		select {
		case s.active <- struct{}{}:
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer func() { <-s.active }()
				s.serveConn(conn)
			}()
		default:
			_ = conn.Close()
			s.Logger.Printf("client refused: concurrency limit")
		}
	}
}

func (s *Server) bind() error {
	return withStartLock(s.Home, func() error {
		if err := s.recoverStale(); err != nil {
			return err
		}
		generation, err := newGeneration()
		if err != nil {
			return err
		}
		socketName, err := socketAddress(s.Home)
		if err != nil {
			return err
		}
		address := &net.UnixAddr{Name: socketName, Net: "unix"}
		listener, err := net.ListenUnix("unix", address)
		if err != nil {
			return fmt.Errorf("bind monitor socket: %w", err)
		}
		if err := os.Chmod(SocketPath(s.Home), 0o600); err != nil {
			listener.Close()
			_ = os.Remove(SocketPath(s.Home))
			return fmt.Errorf("protect monitor socket: %w", err)
		}
		record := RuntimeRecord{Version: runtimeVersion, Generation: generation,
			PID: os.Getpid(), StartedAt: time.Now().UTC()}
		if err := publishRuntime(s.Home, record); err != nil {
			listener.Close()
			_ = os.Remove(SocketPath(s.Home))
			return fmt.Errorf("publish monitor runtime identity: %w", err)
		}
		s.generation = generation
		s.listener = listener
		return nil
	})
}

func (s *Server) recoverStale() error {
	record, runtimeErr := readRuntime(s.Home)
	if runtimeErr == nil {
		client := NewClient(s.Home)
		if _, err := client.Ping(); err == nil {
			return ErrAlreadyRunning
		}
		dead, err := processDefinitelyDead(record.PID)
		if err != nil {
			return fmt.Errorf("stale monitor identity is ambiguous: %w", err)
		}
		if !dead {
			return fmt.Errorf("stale monitor identity is ambiguous: pid %d is alive but its authenticated socket is unavailable", record.PID)
		}
		current, err := readRuntime(s.Home)
		if err != nil || current.Generation != record.Generation || current.PID != record.PID {
			return errors.New("monitor runtime identity changed during stale-instance proof")
		}
		if err := removeSocketIfDead(s.Home); err != nil {
			return err
		}
		if err := os.Remove(RuntimePath(s.Home)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale runtime record: %w", err)
		}
		return nil
	}
	if !errors.Is(runtimeErr, os.ErrNotExist) {
		return fmt.Errorf("monitor runtime identity is unreadable; refusing cleanup: %w", runtimeErr)
	}
	if _, err := os.Lstat(SocketPath(s.Home)); err == nil {
		return removeSocketIfDead(s.Home)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect monitor socket: %w", err)
	}
	return nil
}

func removeSocketIfDead(home string) error {
	path := SocketPath(home)
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if before.Mode()&os.ModeSymlink != 0 || before.Mode()&os.ModeSocket == 0 {
		return errors.New("refusing to remove a non-socket or symlink at the monitor socket path")
	}
	address, err := socketAddress(home)
	if err != nil {
		return err
	}
	conn, dialErr := net.DialTimeout("unix", address, 150*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		return errors.New("an unauthenticated listener is live at the monitor socket path; refusing replacement")
	}
	after, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) {
		return errors.New("monitor socket identity changed during stale-instance proof")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale monitor socket: %w", err)
	}
	return nil
}

func (s *Server) serveConn(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(clientDeadline))
	if err := validatePeer(conn); err != nil {
		s.Logger.Printf("client refused: peer validation failed")
		return
	}
	if !s.allowRequest() {
		s.writeError(conn, "", -32004, "request rate exceeded")
		return
	}
	data, err := readFrame(conn)
	if err != nil {
		s.writeError(conn, "", -32700, "invalid or incomplete frame")
		return
	}
	var req request
	if err := decodeStrict(data, &req); err != nil {
		s.writeError(conn, "", -32700, "invalid JSON request")
		return
	}
	if err := validateRequest(req); err != nil {
		s.writeError(conn, req.ID, -32600, err.Error())
		return
	}
	if s.seenReplay(req.ID) {
		s.writeResult(conn, req.ID, Ack{ProtocolVersion: ProtocolVersion, Status: AckRejected,
			Method: req.Method, Detail: "request id was already used in this monitor generation"})
		return
	}
	s.dispatch(conn, req)
}

func (s *Server) dispatch(conn *net.UnixConn, req request) {
	switch req.Method {
	case MethodPing:
		var params PingParams
		if err := decodeStrict(req.Params, &params); err != nil {
			s.writeError(conn, req.ID, -32602, "invalid ping params")
			return
		}
		if err := validateAuth(params.ProtocolVersion, params.Generation, s.generation); err != nil {
			s.writeResult(conn, req.ID, rejected(req.Method, err))
			return
		}
		s.writeResult(conn, req.ID, PingResult{ProtocolVersion: ProtocolVersion, Status: AckAccepted,
			Capabilities: append([]string(nil), Capabilities...)})
	case MethodStatus:
		var params StatusParams
		if err := decodeStrict(req.Params, &params); err != nil {
			s.writeError(conn, req.ID, -32602, "invalid status params")
			return
		}
		if err := validateAuth(params.ProtocolVersion, params.Generation, s.generation); err != nil {
			s.writeResult(conn, req.ID, rejected(req.Method, err))
			return
		}
		s.writeResult(conn, req.ID, Ack{ProtocolVersion: ProtocolVersion, Status: AckAccepted, Method: req.Method})
	case MethodProgress:
		var params ProgressParams
		if err := decodeStrict(req.Params, &params); err != nil {
			s.writeError(conn, req.ID, -32602, "invalid progress params")
			return
		}
		if err := validateAuth(params.ProtocolVersion, params.Generation, s.generation); err != nil {
			s.writeResult(conn, req.ID, rejected(req.Method, err))
			return
		}
		if params.Note != SanitizeNote(params.Note) || !ValidPhase(params.Phase) {
			s.writeResult(conn, req.ID, rejected(req.Method, errors.New("phase or sanitized bounded note is invalid")))
			return
		}
		if _, err := validateCurrentAttempt(params.TaskID, params.Attempt); err != nil {
			s.writeResult(conn, req.ID, rejected(req.Method, err))
			return
		}
		ack := s.enqueue(Event{Kind: MethodProgress, TaskID: params.TaskID, Attempt: params.Attempt,
			Phase: params.Phase, Note: params.Note})
		s.writeResult(conn, req.ID, ack)
	case MethodTaskChanged:
		var params TaskChangedParams
		if err := decodeStrict(req.Params, &params); err != nil {
			s.writeError(conn, req.ID, -32602, "invalid task_changed params")
			return
		}
		if err := validateAuth(params.ProtocolVersion, params.Generation, s.generation); err != nil {
			s.writeResult(conn, req.ID, rejected(req.Method, err))
			return
		}
		if err := validateCanonicalChange(s.Home, params); err != nil {
			s.writeResult(conn, req.ID, rejected(req.Method, err))
			return
		}
		ack := s.enqueue(Event{Kind: MethodTaskChanged, TaskID: params.TaskID, Attempt: params.Attempt,
			Change: params.Change, ChangeGeneration: params.ChangeGeneration})
		s.writeResult(conn, req.ID, ack)
	case MethodShutdown:
		var params ShutdownParams
		if err := decodeStrict(req.Params, &params); err != nil {
			s.writeError(conn, req.ID, -32602, "invalid shutdown params")
			return
		}
		if err := validateAuth(params.ProtocolVersion, params.Generation, s.generation); err != nil {
			s.writeResult(conn, req.ID, rejected(req.Method, err))
			return
		}
		s.writeResult(conn, req.ID, Ack{ProtocolVersion: ProtocolVersion, Status: AckAccepted, Method: req.Method})
		s.initiateShutdown()
	default:
		s.writeError(conn, req.ID, -32601, "method not found")
	}
}

func rejected(method string, err error) Ack {
	return Ack{ProtocolVersion: ProtocolVersion, Status: AckRejected, Method: method, Detail: err.Error()}
}

func (s *Server) enqueue(event Event) Ack {
	key := fmt.Sprintf("%s/%d", event.TaskID, event.Attempt)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.pending[key]; ok {
		if stronger(event, existing.event) {
			existing.event = event
		}
		return Ack{ProtocolVersion: ProtocolVersion, Status: AckCoalesced, Method: event.Kind,
			Detail: "coalesced with a pending exact task/attempt notification"}
	}
	if len(s.pending) >= maxPending {
		return Ack{ProtocolVersion: ProtocolVersion, Status: AckRejected, Method: event.Kind,
			Detail: "monitor pending notification queue is full"}
	}
	pending := &pendingEvent{event: event}
	s.timers.Add(1)
	pending.timer = time.AfterFunc(s.CoalesceDelay, func() {
		defer s.timers.Done()
		s.forward(key)
	})
	s.pending[key] = pending
	return Ack{ProtocolVersion: ProtocolVersion, Status: AckAccepted, Method: event.Kind}
}

func stronger(candidate, existing Event) bool {
	if candidate.Kind == MethodTaskChanged && existing.Kind != MethodTaskChanged {
		return true
	}
	if candidate.Kind != existing.Kind {
		return false
	}
	if candidate.Kind == MethodProgress {
		return progressRank(candidate.Phase) >= progressRank(existing.Phase)
	}
	return changeRank(candidate.Change) >= changeRank(existing.Change)
}

func progressRank(phase string) int {
	switch phase {
	case PhaseBlocked:
		return 50
	case PhaseWaiting:
		return 40
	case PhaseTesting:
		return 30
	case PhaseImplementing:
		return 20
	default:
		return 10
	}
}

func changeRank(change string) int {
	switch change {
	case ChangeRelease:
		return 60
	case ChangeDelivery:
		return 50
	case ChangeValidation:
		return 40
	case ChangeReview:
		return 45
	case ChangeVerification:
		return 30
	case ChangeReport:
		return 25
	default:
		return 20
	}
}

func (s *Server) forward(key string) {
	s.mu.Lock()
	pending, ok := s.pending[key]
	if ok {
		delete(s.pending, key)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if err := s.validateEvent(pending.event); err != nil {
		s.Logger.Printf("forward dropped after filesystem revalidation task=%s attempt=%d kind=%s",
			pending.event.TaskID, pending.event.Attempt, pending.event.Kind)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Forwarder.Forward(ctx, pending.event); err != nil {
		s.Logger.Printf("forward unavailable task=%s attempt=%d kind=%s", pending.event.TaskID, pending.event.Attempt, pending.event.Kind)
	}
}

func (s *Server) flushPending() {
	s.mu.Lock()
	items := make([]Event, 0, len(s.pending))
	for key, pending := range s.pending {
		if pending.timer.Stop() {
			s.timers.Done()
		}
		items = append(items, pending.event)
		delete(s.pending, key)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, event := range items {
		if ctx.Err() != nil {
			break
		}
		if err := s.validateEvent(event); err != nil {
			s.Logger.Printf("shutdown forward dropped after filesystem revalidation task=%s attempt=%d kind=%s",
				event.TaskID, event.Attempt, event.Kind)
			continue
		}
		if err := s.Forwarder.Forward(ctx, event); err != nil {
			s.Logger.Printf("shutdown forward unavailable task=%s attempt=%d kind=%s", event.TaskID, event.Attempt, event.Kind)
		}
	}
}

func (s *Server) validateEvent(event Event) error {
	if event.Kind == MethodProgress {
		_, err := validateCurrentAttempt(event.TaskID, event.Attempt)
		return err
	}
	return validateCanonicalChange(s.Home, TaskChangedParams{TaskID: event.TaskID, Attempt: event.Attempt,
		Change: event.Change, ChangeGeneration: event.ChangeGeneration})
}

func (s *Server) seenReplay(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.replays[id]; ok {
		return true
	}
	s.replays[id] = struct{}{}
	s.replayOrder = append(s.replayOrder, id)
	if len(s.replayOrder) > maxReplayIDs {
		delete(s.replays, s.replayOrder[0])
		s.replayOrder = s.replayOrder[1:]
	}
	return false
}

func (s *Server) allowRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.rateStart.IsZero() || now.Sub(s.rateStart) >= time.Second {
		s.rateStart = now
		s.rateCount = 0
	}
	s.rateCount++
	return s.rateCount <= 128
}

func (s *Server) writeResult(conn net.Conn, id string, result any) {
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = writeFrame(conn, response{JSONRPC: JSONRPCVersion, ID: id, Result: data})
}

func (s *Server) writeError(conn net.Conn, id string, code int, message string) {
	_ = writeFrame(conn, response{JSONRPC: JSONRPCVersion, ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (s *Server) initiateShutdown() {
	s.shutdownOnce.Do(func() {
		close(s.shutdown)
		if s.listener != nil {
			_ = s.listener.Close()
		}
	})
}

func (s *Server) cleanup() {
	_ = withStartLock(s.Home, func() error {
		record, err := readRuntime(s.Home)
		if err != nil || record.Generation != s.generation || record.PID != os.Getpid() {
			return nil
		}
		if info, err := os.Lstat(SocketPath(s.Home)); err == nil && info.Mode()&os.ModeSocket != 0 {
			_ = os.Remove(SocketPath(s.Home))
		}
		current, err := readRuntime(s.Home)
		if err == nil && current.Generation == s.generation && current.PID == os.Getpid() {
			_ = os.Remove(RuntimePath(s.Home))
		}
		cleanupSocketAlias(s.Home)
		return nil
	})
}

// Retained to make explicit that process shutdown never targets a PID. The
// monitor uses authenticated RPC and this signal is handled only in-process.
var _ = syscall.SIGTERM
