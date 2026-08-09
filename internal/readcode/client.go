// Package readcode implements Sophon's only boundary to the standalone Read
// the Code product: its documented executable and versioned JSON contract.
// It does not import product modules or inspect product state files.
package readcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion = 1
	MaxJSONBytes  = 8 << 20
	MaxErrorBytes = 4 << 10
)

var (
	ErrOutputLimit   = errors.New("read-the-code-axi output exceeded the bounded JSON limit")
	shaPattern       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sessionPattern   = regexp.MustCompile(`^[0-9a-f]{24}$`)
	uuidPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	hashPattern      = regexp.MustCompile(`^[0-9a-f]{24}$`)
	errorCodePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

type Product interface {
	Open(context.Context, OpenRequest) (OpenResult, error)
	Status(context.Context, string) (StatusResult, error)
	Poll(context.Context, string, int, time.Duration) (PollResult, error)
	End(context.Context, string) (EndResult, error)
}

type Client struct {
	Binary string
}

type OpenRequest struct {
	Repository string
	BaseSHA    string
	HeadSHA    string
	NoBrowser  bool
}

type OpenResult struct {
	SchemaVersion int    `json:"schemaVersion"`
	SessionID     string `json:"sessionId"`
	BaseSHA       string `json:"baseSha"`
	HeadSHA       string `json:"headSha"`
	BrowserURL    string `json:"browserUrl"`
	Resumed       bool   `json:"resumed"`
	Status        string `json:"status"`
}

type ReviewSummary struct {
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

type StatusResult struct {
	SchemaVersion int           `json:"schemaVersion"`
	SessionID     string        `json:"sessionId"`
	Status        string        `json:"status"`
	Stale         bool          `json:"stale"`
	ApprovalStale bool          `json:"approvalStale"`
	BaseSHA       string        `json:"baseSha"`
	HeadSHA       string        `json:"headSha"`
	Summary       ReviewSummary `json:"summary"`
	EventCount    int           `json:"eventCount"`
	LastSequence  int           `json:"lastSequence"`
	UpdatedAt     string        `json:"updatedAt"`
}

type Revision struct {
	BaseSHA string `json:"baseSha"`
	HeadSHA string `json:"headSha"`
}

type Anchor struct {
	Revision       Revision `json:"revision"`
	Path           string   `json:"path"`
	Side           string   `json:"side"`
	StartLine      int      `json:"startLine"`
	EndLine        int      `json:"endLine"`
	ContextHash    string   `json:"contextHash"`
	EndContextHash string   `json:"endContextHash"`
}

type Comment struct {
	ID        string  `json:"id"`
	Scope     string  `json:"scope"`
	Body      string  `json:"body"`
	Path      string  `json:"path,omitempty"`
	Anchor    *Anchor `json:"anchor,omitempty"`
	CreatedAt string  `json:"createdAt"`
}

type Event struct {
	SchemaVersion   int       `json:"schemaVersion"`
	SessionID       string    `json:"sessionId"`
	Sequence        int       `json:"sequence"`
	ID              string    `json:"id"`
	CreatedAt       string    `json:"createdAt"`
	BaseSHA         string    `json:"baseSha"`
	HeadSHA         string    `json:"headSha"`
	Type            string    `json:"type"`
	Comments        []Comment `json:"comments,omitempty"`
	ApprovedHeadSHA string    `json:"approvedHeadSha,omitempty"`
}

type PollResult struct {
	SchemaVersion int     `json:"schemaVersion"`
	SessionID     string  `json:"sessionId"`
	After         int     `json:"after"`
	NextCursor    int     `json:"nextCursor"`
	TimedOut      bool    `json:"timedOut"`
	Events        []Event `json:"events"`
}

type EndResult struct {
	SchemaVersion int    `json:"schemaVersion"`
	SessionID     string `json:"sessionId"`
	Status        string `json:"status"`
	Event         Event  `json:"event"`
}

func (c Client) Open(ctx context.Context, request OpenRequest) (OpenResult, error) {
	if strings.TrimSpace(c.Binary) == "" {
		return OpenResult{}, errors.New("Read the Code executable is not configured; use --read-the-code or SOPHON_READ_THE_CODE")
	}
	if request.Repository == "" || !shaPattern.MatchString(request.BaseSHA) || !shaPattern.MatchString(request.HeadSHA) ||
		request.BaseSHA == request.HeadSHA {
		return OpenResult{}, errors.New("Read the Code open requires a repository and two distinct lowercase 40-character SHAs")
	}
	args := []string{"open", "--repo", request.Repository, "--base", request.BaseSHA, "--head", request.HeadSHA, "--json"}
	if request.NoBrowser {
		args = append(args, "--no-browser")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var result OpenResult
	if err := c.runJSON(commandCtx, args, &result); err != nil {
		return OpenResult{}, err
	}
	if result.SchemaVersion != SchemaVersion || !sessionPattern.MatchString(result.SessionID) ||
		result.BaseSHA != request.BaseSHA || result.HeadSHA != request.HeadSHA || result.Status != "open" {
		return OpenResult{}, errors.New("Read the Code open returned an unsupported schema or mismatched session/revision")
	}
	if err := validateCapabilityURL(result.BrowserURL, result.SessionID); err != nil {
		return OpenResult{}, err
	}
	return result, nil
}

func (c Client) Status(ctx context.Context, sessionID string) (StatusResult, error) {
	if strings.TrimSpace(c.Binary) == "" {
		return StatusResult{}, errors.New("Read the Code executable is not configured; use --read-the-code or SOPHON_READ_THE_CODE")
	}
	if !sessionPattern.MatchString(sessionID) {
		return StatusResult{}, errors.New("invalid Read the Code session id")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var result StatusResult
	if err := c.runJSON(commandCtx, []string{"status", sessionID, "--json"}, &result); err != nil {
		return StatusResult{}, err
	}
	if result.SchemaVersion != SchemaVersion || result.SessionID != sessionID ||
		(result.Status != "open" && result.Status != "ended") || !shaPattern.MatchString(result.BaseSHA) ||
		!shaPattern.MatchString(result.HeadSHA) || result.EventCount < 0 || result.LastSequence < 0 ||
		result.EventCount != result.LastSequence || result.Summary.Files < 0 || result.Summary.Additions < 0 ||
		result.Summary.Deletions < 0 {
		return StatusResult{}, errors.New("Read the Code status returned an unsupported or internally inconsistent schema")
	}
	if _, err := time.Parse(time.RFC3339Nano, result.UpdatedAt); err != nil {
		return StatusResult{}, errors.New("Read the Code status returned an invalid timestamp")
	}
	return result, nil
}

func (c Client) Poll(ctx context.Context, sessionID string, after int, timeout time.Duration) (PollResult, error) {
	if strings.TrimSpace(c.Binary) == "" {
		return PollResult{}, errors.New("Read the Code executable is not configured; use --read-the-code or SOPHON_READ_THE_CODE")
	}
	if !sessionPattern.MatchString(sessionID) || after < 0 || timeout < 0 || timeout > time.Hour {
		return PollResult{}, errors.New("Read the Code poll requires a valid session, cursor, and timeout of at most one hour")
	}
	duration := strconv.FormatInt(timeout.Milliseconds(), 10) + "ms"
	commandCtx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()
	var result PollResult
	if err := c.runJSON(commandCtx, []string{"poll", sessionID, "--after", strconv.Itoa(after), "--timeout", duration, "--json"}, &result); err != nil {
		return PollResult{}, err
	}
	if err := validatePoll(result, sessionID, after); err != nil {
		return PollResult{}, err
	}
	return result, nil
}

func (c Client) End(ctx context.Context, sessionID string) (EndResult, error) {
	if strings.TrimSpace(c.Binary) == "" {
		return EndResult{}, errors.New("Read the Code executable is not configured; use --read-the-code or SOPHON_READ_THE_CODE")
	}
	if !sessionPattern.MatchString(sessionID) {
		return EndResult{}, errors.New("invalid Read the Code session id")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var result EndResult
	if err := c.runJSON(commandCtx, []string{"end", sessionID, "--json"}, &result); err != nil {
		return EndResult{}, err
	}
	if result.SchemaVersion != SchemaVersion || result.SessionID != sessionID || result.Status != "ended" {
		return EndResult{}, errors.New("Read the Code end returned an unsupported or mismatched schema")
	}
	if err := validateEvent(result.Event, sessionID); err != nil || result.Event.Type != "end" {
		return EndResult{}, errors.New("Read the Code end returned invalid end evidence")
	}
	return result, nil
}

func validatePoll(result PollResult, sessionID string, after int) error {
	if result.SchemaVersion != SchemaVersion || result.SessionID != sessionID || result.After != after ||
		result.NextCursor < after || (result.TimedOut && len(result.Events) != 0) {
		return errors.New("Read the Code poll returned an unsupported or mismatched envelope")
	}
	if len(result.Events) == 0 {
		if result.NextCursor != after {
			return errors.New("Read the Code poll advanced an empty cursor")
		}
		return nil
	}
	if result.TimedOut {
		return errors.New("Read the Code poll marked a non-empty event batch as timed out")
	}
	eventIDs := make(map[string]struct{}, len(result.Events))
	for index, event := range result.Events {
		want := after + index + 1
		if event.Sequence != want {
			return fmt.Errorf("Read the Code poll sequence gap: got %d, want %d", event.Sequence, want)
		}
		if err := validateEvent(event, sessionID); err != nil {
			return err
		}
		if _, duplicate := eventIDs[event.ID]; duplicate {
			return errors.New("Read the Code poll contains a duplicate event id")
		}
		eventIDs[event.ID] = struct{}{}
		if index > 0 && result.Events[index-1].Type == "end" {
			return errors.New("Read the Code poll contains an event after terminal end")
		}
	}
	if result.NextCursor != result.Events[len(result.Events)-1].Sequence {
		return errors.New("Read the Code poll next cursor does not equal the final event sequence")
	}
	return nil
}

func validateEvent(event Event, sessionID string) error {
	if event.SchemaVersion != SchemaVersion || event.SessionID != sessionID || event.Sequence < 1 ||
		!uuidPattern.MatchString(event.ID) || !shaPattern.MatchString(event.BaseSHA) ||
		!shaPattern.MatchString(event.HeadSHA) {
		return errors.New("Read the Code event has invalid schema or identity")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.CreatedAt); err != nil {
		return errors.New("Read the Code event has an invalid timestamp")
	}
	switch event.Type {
	case "approval":
		if event.ApprovedHeadSHA != event.HeadSHA || event.Comments != nil {
			return errors.New("Read the Code approval is not bound to its exact head")
		}
	case "end":
		if event.ApprovedHeadSHA != "" || event.Comments != nil {
			return errors.New("Read the Code end contains unexpected fields")
		}
	case "feedback":
		if event.ApprovedHeadSHA != "" || len(event.Comments) < 1 || len(event.Comments) > 100 {
			return errors.New("Read the Code feedback has an invalid comment batch")
		}
		if err := validateComments(event.Comments, event.BaseSHA, event.HeadSHA); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown Read the Code event type %q", event.Type)
	}
	return nil
}

func validateComments(comments []Comment, baseSHA, headSHA string) error {
	ids := make(map[string]struct{}, len(comments))
	total := 0
	for _, comment := range comments {
		if !uuidPattern.MatchString(comment.ID) || comment.Body == "" || len(comment.Body) > 20<<10 ||
			!utf8.ValidString(comment.Body) || unsafeControl(comment.Body) {
			return errors.New("Read the Code comment has invalid identity or text")
		}
		if _, exists := ids[comment.ID]; exists {
			return errors.New("Read the Code comment batch contains a duplicate id")
		}
		ids[comment.ID] = struct{}{}
		if _, err := time.Parse(time.RFC3339Nano, comment.CreatedAt); err != nil {
			return errors.New("Read the Code comment has an invalid timestamp")
		}
		total += len(comment.Body)
		switch comment.Scope {
		case "general":
			if comment.Path != "" || comment.Anchor != nil {
				return errors.New("Read the Code general comment contains a path or anchor")
			}
		case "file":
			if !safePath(comment.Path) || comment.Anchor != nil {
				return errors.New("Read the Code file comment has an invalid path or anchor")
			}
		case "line":
			anchor := comment.Anchor
			if !safePath(comment.Path) || anchor == nil || anchor.Path != comment.Path ||
				anchor.Revision.BaseSHA != baseSHA || anchor.Revision.HeadSHA != headSHA ||
				(anchor.Side != "old" && anchor.Side != "new") || anchor.StartLine < 1 || anchor.EndLine < anchor.StartLine ||
				anchor.EndLine > 10_000_000 ||
				!hashPattern.MatchString(anchor.ContextHash) || !hashPattern.MatchString(anchor.EndContextHash) {
				return errors.New("Read the Code line comment has an invalid exact-revision anchor")
			}
		default:
			return errors.New("Read the Code comment has an unknown scope")
		}
	}
	if total > 100<<10 {
		return errors.New("Read the Code feedback text exceeds the documented batch limit")
	}
	return nil
}

func safePath(value string) bool {
	if value == "" || len(value) > 4096 || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || unsafeControl(value) {
		return false
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func unsafeControl(value string) bool {
	for _, r := range value {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return true
		}
	}
	return false
}

func validateCapabilityURL(raw, sessionID string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment == "" ||
		!strings.HasPrefix(parsed.Fragment, "/review/"+sessionID+"/") {
		return errors.New("Read the Code open returned an invalid loopback capability URL")
	}
	return nil
}

func (c Client) runJSON(ctx context.Context, args []string, target any) error {
	command := exec.CommandContext(ctx, c.Binary, args...)
	// Give the exact CLI invocation its own process group. On cancellation,
	// terminate only that group so a crashed/hung wrapper cannot strand child
	// processes holding stdout/stderr open or outlive its bounded contract.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else {
			return err
		}
	}
	command.WaitDelay = time.Second
	var stdout, stderr limitedBuffer
	stdout.limit = MaxJSONBytes
	stderr.limit = MaxErrorBytes
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.Stdin = nil
	err := command.Run()
	if errors.Is(stdout.err, ErrOutputLimit) || errors.Is(stderr.err, ErrOutputLimit) {
		return ErrOutputLimit
	}
	if err != nil {
		return commandFailure(err, stderr.Bytes())
	}
	if len(bytes.TrimSpace(stderr.Bytes())) != 0 {
		return errors.New("read-the-code-axi wrote unexpected stderr on success")
	}
	if !utf8.Valid(stdout.Bytes()) {
		return errors.New("read-the-code-axi returned non-UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := decoder.Decode(target); err != nil {
		return errors.New("read-the-code-axi returned malformed versioned JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("read-the-code-axi returned multiple JSON values")
		}
		return errors.New("read-the-code-axi returned malformed trailing output")
	}
	return nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
	err    error
}

func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }
func (b *limitedBuffer) Len() int      { return b.buffer.Len() }

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	remaining := b.limit - b.Len()
	if len(data) > remaining {
		if remaining > 0 {
			_, _ = b.buffer.Write(data[:remaining])
		}
		b.err = ErrOutputLimit
		return len(data), b.err
	}
	return b.buffer.Write(data)
}

func commandFailure(runErr error, stderr []byte) error {
	var payload struct {
		SchemaVersion int `json:"schemaVersion"`
		Error         struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(stderr, &payload) == nil && payload.SchemaVersion == SchemaVersion && payload.Error.Code != "" {
		code := boundedDiagnostic(payload.Error.Code)
		if !errorCodePattern.MatchString(code) {
			code = "failure"
		}
		return fmt.Errorf("read-the-code-axi %s", code)
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return fmt.Errorf("read-the-code-axi exited with status %d", exitErr.ExitCode())
	}
	return fmt.Errorf("start read-the-code-axi: %w", runErr)
}

func boundedDiagnostic(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 256 {
		value = value[:256]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}
