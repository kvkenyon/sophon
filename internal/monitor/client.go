package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

var ErrUnavailable = errors.New("notification monitor unavailable")

type Client struct {
	home    string
	timeout time.Duration
}

func NewClient(home string) *Client { return &Client{home: home, timeout: 2 * time.Second} }

func (c *Client) Ping() (PingResult, error) {
	record, err := readRuntime(c.home)
	if err != nil {
		return PingResult{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var result PingResult
	if err := c.call(record, MethodPing, PingParams{ProtocolVersion: ProtocolVersion, Generation: record.Generation}, &result); err != nil {
		return PingResult{}, err
	}
	if result.ProtocolVersion != ProtocolVersion || result.Status != AckAccepted {
		return PingResult{}, fmt.Errorf("%w: incompatible ping response", ErrUnavailable)
	}
	return result, nil
}

func (c *Client) Progress(taskID string, attempt int, phase, note string) (Ack, error) {
	record, err := readRuntime(c.home)
	if err != nil {
		return unavailableAck(MethodProgress, err), fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	params := ProgressParams{ProtocolVersion: ProtocolVersion, Generation: record.Generation,
		TaskID: taskID, Attempt: attempt, Phase: phase, Note: SanitizeNote(note)}
	var result Ack
	if err := c.call(record, MethodProgress, params, &result); err != nil {
		return unavailableAck(MethodProgress, err), err
	}
	return result, nil
}

func (c *Client) TaskChanged(taskID string, attempt int, change, generation string) (Ack, error) {
	record, err := readRuntime(c.home)
	if err != nil {
		return unavailableAck(MethodTaskChanged, err), fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	params := TaskChangedParams{ProtocolVersion: ProtocolVersion, Generation: record.Generation,
		TaskID: taskID, Attempt: attempt, Change: change, ChangeGeneration: generation}
	var result Ack
	if err := c.call(record, MethodTaskChanged, params, &result); err != nil {
		return unavailableAck(MethodTaskChanged, err), err
	}
	return result, nil
}

func (c *Client) Shutdown() (Ack, error) {
	record, err := readRuntime(c.home)
	if err != nil {
		return unavailableAck(MethodShutdown, err), fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var result Ack
	if err := c.call(record, MethodShutdown, ShutdownParams{ProtocolVersion: ProtocolVersion, Generation: record.Generation}, &result); err != nil {
		return unavailableAck(MethodShutdown, err), err
	}
	return result, nil
}

func unavailableAck(method string, err error) Ack {
	return Ack{ProtocolVersion: ProtocolVersion, Status: AckUnavailable, Method: method, Detail: err.Error()}
}

func (c *Client) call(record RuntimeRecord, method string, params any, result any) error {
	info, err := os.Lstat(SocketPath(c.home))
	if err != nil {
		return fmt.Errorf("%w: inspect socket: %v", ErrUnavailable, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: monitor socket is not a private Unix socket", ErrUnavailable)
	}
	address, err := socketAddress(c.home)
	if err != nil {
		return fmt.Errorf("%w: resolve socket address: %v", ErrUnavailable, err)
	}
	conn, err := net.DialTimeout("unix", address, c.timeout)
	if err != nil {
		return fmt.Errorf("%w: connect: %v", ErrUnavailable, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.timeout))
	requestID, err := newRequestID()
	if err != nil {
		return err
	}
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req := request{JSONRPC: JSONRPCVersion, Method: method, ID: requestID, Params: encodedParams}
	if err := writeFrame(conn, req); err != nil {
		return fmt.Errorf("%w: send request: %v", ErrUnavailable, err)
	}
	data, err := readFrame(conn)
	if err != nil {
		return fmt.Errorf("%w: read response: %v", ErrUnavailable, err)
	}
	var resp response
	if err := decodeStrict(data, &resp); err != nil {
		return fmt.Errorf("%w: decode response: %v", ErrUnavailable, err)
	}
	if resp.JSONRPC != JSONRPCVersion || resp.ID != requestID || (len(resp.Result) == 0) == (resp.Error == nil) {
		return fmt.Errorf("%w: invalid JSON-RPC response envelope", ErrUnavailable)
	}
	if resp.Error != nil {
		return fmt.Errorf("monitor RPC %s failed (%d): %s", method, resp.Error.Code, resp.Error.Message)
	}
	if err := decodeStrict(resp.Result, result); err != nil {
		return fmt.Errorf("%w: decode result: %v", ErrUnavailable, err)
	}
	return nil
}
