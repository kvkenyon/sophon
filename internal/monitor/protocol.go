// Package monitor implements Sophon's optional local notification transport.
// It carries no lifecycle truth: every task change is validated against the
// canonical filesystem records before it can be forwarded.
package monitor

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	JSONRPCVersion     = "2.0"
	ProtocolVersion    = 1
	MaxFrameSize       = 16 << 10
	MaxNoteLength      = 256
	MaxRequestIDLength = 64
)

const (
	MethodPing        = "monitor.ping"
	MethodProgress    = "notify.progress"
	MethodTaskChanged = "notify.task_changed"
	MethodStatus      = "monitor.status"
	MethodShutdown    = "monitor.shutdown"
)

var Capabilities = []string{MethodPing, MethodProgress, MethodTaskChanged, MethodStatus, MethodShutdown}

type AckStatus string

const (
	AckAccepted    AckStatus = "accepted"
	AckCoalesced   AckStatus = "coalesced"
	AckRejected    AckStatus = "rejected"
	AckUnavailable AckStatus = "unavailable"
)

type Ack struct {
	ProtocolVersion int       `json:"protocol_version"`
	Status          AckStatus `json:"status"`
	Method          string    `json:"method"`
	Detail          string    `json:"detail,omitempty"`
}

type PingResult struct {
	ProtocolVersion int       `json:"protocol_version"`
	Status          AckStatus `json:"status"`
	Capabilities    []string  `json:"capabilities"`
}

type PublicStatus struct {
	ProtocolVersion int      `json:"protocol_version"`
	Running         bool     `json:"running"`
	Status          string   `json:"status"`
	PID             int      `json:"pid,omitempty"`
	StartedAt       string   `json:"started_at,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Detail          string   `json:"detail,omitempty"`
}

type authParams struct {
	ProtocolVersion int    `json:"protocol_version"`
	Generation      string `json:"generation"`
}

type PingParams authParams

type ProgressParams struct {
	ProtocolVersion int    `json:"protocol_version"`
	Generation      string `json:"generation"`
	TaskID          string `json:"task_id"`
	Attempt         int    `json:"attempt"`
	Phase           string `json:"phase"`
	Note            string `json:"note,omitempty"`
}

type TaskChangedParams struct {
	ProtocolVersion  int    `json:"protocol_version"`
	Generation       string `json:"generation"`
	TaskID           string `json:"task_id"`
	Attempt          int    `json:"attempt"`
	Change           string `json:"change"`
	ChangeGeneration string `json:"change_generation"`
}

type StatusParams authParams

type ShutdownParams authParams

const (
	PhaseInvestigating = "investigating"
	PhaseImplementing  = "implementing"
	PhaseTesting       = "testing"
	PhaseWaiting       = "waiting"
	PhaseBlocked       = "blocked"
)

var phases = map[string]bool{
	PhaseInvestigating: true,
	PhaseImplementing:  true,
	PhaseTesting:       true,
	PhaseWaiting:       true,
	PhaseBlocked:       true,
}

const (
	ChangeCompletion   = "completion"
	ChangeReport       = "report"
	ChangeVerification = "verification"
	ChangeValidation   = "validation"
	ChangeReview       = "review"
	ChangeDelivery     = "delivery"
	ChangeRelease      = "release"
)

var changes = map[string]bool{
	ChangeCompletion:   true,
	ChangeReport:       true,
	ChangeVerification: true,
	ChangeValidation:   true,
	ChangeReview:       true,
	ChangeDelivery:     true,
	ChangeRelease:      true,
}

var (
	safeTaskID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	safeGeneration = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func ValidPhase(phase string) bool   { return phases[phase] }
func ValidChange(change string) bool { return changes[change] }

// SanitizeNote reduces a worker-authored note to one bounded printable line.
// The result is inert prose and is never interpreted as a command.
func SanitizeNote(note string) string {
	var cleaned strings.Builder
	for _, r := range note {
		if r < 0x20 || r == 0x7f {
			cleaned.WriteByte(' ')
			continue
		}
		cleaned.WriteRune(r)
	}
	value := strings.Join(strings.Fields(cleaned.String()), " ")
	if len(value) > MaxNoteLength {
		value = value[:MaxNoteLength]
		for len(value) > 0 && !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func newGeneration() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate monitor generation: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func newRequestID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      string          `json:"id"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func validateRequest(req request) error {
	if req.JSONRPC != JSONRPCVersion {
		return errors.New("jsonrpc must be exactly \"2.0\"")
	}
	if req.Method == "" || len(req.Method) > 64 {
		return errors.New("method is required and bounded to 64 bytes")
	}
	if req.ID == "" || len(req.ID) > MaxRequestIDLength {
		return errors.New("id must be a non-empty string bounded to 64 bytes")
	}
	if len(req.Params) == 0 || bytes.Equal(req.Params, []byte("null")) {
		return errors.New("params must be an object")
	}
	return nil
}

func validateAuth(version int, generation, expected string) error {
	if version != ProtocolVersion {
		return fmt.Errorf("protocol_version must be %d", ProtocolVersion)
	}
	if !safeGeneration.MatchString(generation) || generation != expected {
		return errors.New("monitor generation is invalid or stale")
	}
	return nil
}

func writeFrame(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > MaxFrameSize {
		return fmt.Errorf("JSON-RPC frame size %d exceeds limit %d", len(data), MaxFrameSize)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if err := writeAll(writer, header[:]); err != nil {
		return err
	}
	return writeAll(writer, data)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func readFrame(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameSize {
		return nil, fmt.Errorf("JSON-RPC frame size %d is outside 1..%d", size, MaxFrameSize)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(reader, data); err != nil {
		return nil, err
	}
	return data, nil
}
