package readcode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	testSession = "57d91f3ddc544f34e70c1156"
	testBase    = "1111111111111111111111111111111111111111"
	testHead    = "2222222222222222222222222222222222222222"
)

func TestClientOpenUsesStrictVersionedJSON(t *testing.T) {
	script := writeScript(t, `#!/bin/sh
printf '%s\n' '{"schemaVersion":1,"sessionId":"57d91f3ddc544f34e70c1156","baseSha":"1111111111111111111111111111111111111111","headSha":"2222222222222222222222222222222222222222","browserUrl":"http://127.0.0.1:49152/#/review/57d91f3ddc544f34e70c1156/token","resumed":false,"status":"open"}'
`)
	result, err := (Client{Binary: script}).Open(context.Background(), OpenRequest{Repository: "/repo",
		BaseSHA: testBase, HeadSHA: testHead, NoBrowser: true})
	if err != nil || result.SessionID != testSession {
		t.Fatalf("open = %+v, %v", result, err)
	}

	additive := writeScript(t, `#!/bin/sh
printf '%s\n' '{"schemaVersion":1,"sessionId":"57d91f3ddc544f34e70c1156","baseSha":"1111111111111111111111111111111111111111","headSha":"2222222222222222222222222222222222222222","browserUrl":"http://127.0.0.1:1/#/review/57d91f3ddc544f34e70c1156/token","resumed":false,"status":"open","capability":"leak"}'
`)
	if _, err := (Client{Binary: additive}).Open(context.Background(), OpenRequest{Repository: "/repo",
		BaseSHA: testBase, HeadSHA: testHead}); err != nil {
		t.Fatalf("schema-version-1 additive field was not forward compatible: %v", err)
	}

	stderr := writeScript(t, `#!/bin/sh
printf '%s\n' '{"schemaVersion":1,"sessionId":"57d91f3ddc544f34e70c1156","baseSha":"1111111111111111111111111111111111111111","headSha":"2222222222222222222222222222222222222222","browserUrl":"http://127.0.0.1:1/#/review/57d91f3ddc544f34e70c1156/token","resumed":false,"status":"open"}'
printf 'unexpected' >&2
`)
	if _, err := (Client{Binary: stderr}).Open(context.Background(), OpenRequest{Repository: "/repo",
		BaseSHA: testBase, HeadSHA: testHead}); err == nil || !strings.Contains(err.Error(), "unexpected stderr") {
		t.Fatalf("stderr success error = %v", err)
	}
}

func TestPollValidationRefusesReplayGapsHostileCommentsAndDrift(t *testing.T) {
	base := Event{SchemaVersion: 1, SessionID: testSession, Sequence: 1,
		ID: "11111111-1111-4111-8111-111111111111", CreatedAt: "2026-08-08T12:00:00Z",
		BaseSHA: testBase, HeadSHA: testHead, Type: "feedback", Comments: []Comment{{
			ID: "22222222-2222-4222-8222-222222222222", Scope: "general", Body: "bounded feedback", CreatedAt: "2026-08-08T12:00:00Z"}}}
	valid := PollResult{SchemaVersion: 1, SessionID: testSession, After: 0, NextCursor: 1, Events: []Event{base}}
	if err := validatePoll(valid, testSession, 0); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*PollResult)
	}{
		{"unsupported schema", func(result *PollResult) { result.SchemaVersion = 2 }},
		{"session replacement", func(result *PollResult) { result.SessionID = "aaaaaaaaaaaaaaaaaaaaaaaa" }},
		{"sequence gap", func(result *PollResult) { result.Events[0].Sequence = 2; result.NextCursor = 2 }},
		{"cursor lie", func(result *PollResult) { result.NextCursor = 2 }},
		{"unknown event", func(result *PollResult) { result.Events[0].Type = "merge" }},
		{"malicious path", func(result *PollResult) {
			result.Events[0].Comments[0].Scope = "file"
			result.Events[0].Comments[0].Path = "../../secret"
		}},
		{"control body", func(result *PollResult) { result.Events[0].Comments[0].Body = "bad\x00body" }},
		{"line bound", func(result *PollResult) {
			result.Events[0].Comments[0].Scope = "line"
			result.Events[0].Comments[0].Path = "README.md"
			result.Events[0].Comments[0].Anchor = &Anchor{Revision: Revision{BaseSHA: testBase, HeadSHA: testHead},
				Path: "README.md", Side: "new", StartLine: 1, EndLine: 10_000_001,
				ContextHash: "aaaaaaaaaaaaaaaaaaaaaaaa", EndContextHash: "aaaaaaaaaaaaaaaaaaaaaaaa"}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := valid
			result.Events = append([]Event(nil), valid.Events...)
			result.Events[0].Comments = append([]Comment(nil), valid.Events[0].Comments...)
			test.edit(&result)
			if err := validatePoll(result, testSession, 0); err == nil {
				t.Fatal("malformed poll was accepted")
			}
		})
	}
	second := Event{SchemaVersion: 1, SessionID: testSession, Sequence: 2, ID: base.ID,
		CreatedAt: "2026-08-08T12:01:00Z", BaseSHA: testBase, HeadSHA: testHead,
		Type: "approval", ApprovedHeadSHA: testHead}
	if err := validatePoll(PollResult{SchemaVersion: 1, SessionID: testSession, After: 0,
		NextCursor: 2, Events: []Event{base, second}}, testSession, 0); err == nil {
		t.Fatal("duplicate product event id was accepted")
	}
	ended := Event{SchemaVersion: 1, SessionID: testSession, Sequence: 1,
		ID: "33333333-3333-4333-8333-333333333333", CreatedAt: "2026-08-08T12:00:30Z",
		BaseSHA: testBase, HeadSHA: testHead, Type: "end"}
	second.ID = "44444444-4444-4444-8444-444444444444"
	if err := validatePoll(PollResult{SchemaVersion: 1, SessionID: testSession, After: 0,
		NextCursor: 2, Events: []Event{ended, second}}, testSession, 0); err == nil {
		t.Fatal("event after terminal end was accepted")
	}
}

func TestClientBoundsTimeoutOutputAndDiagnostics(t *testing.T) {
	var buffer limitedBuffer
	buffer.limit = 4
	if _, err := buffer.Write([]byte("12345")); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("limit error = %v", err)
	}
	client := Client{Binary: "/not/used"}
	if _, err := client.Poll(context.Background(), testSession, 0, time.Hour+time.Millisecond); err == nil {
		t.Fatal("unbounded poll timeout accepted")
	}
	if got := boundedDiagnostic("hello\n\x00 world"); strings.ContainsAny(got, "\n\x00") {
		t.Fatalf("diagnostic was not one printable line: %q", got)
	}
	if err := validateCapabilityURL("http://example.com/#/review/"+testSession+"/token", testSession); err == nil {
		t.Fatal("non-loopback capability URL accepted")
	}
}

func TestClientRefusesCrashPartialOversizeAndHostileDiagnostics(t *testing.T) {
	request := OpenRequest{Repository: "/repo", BaseSHA: testBase, HeadSHA: testHead, NoBrowser: true}
	cases := []struct {
		name   string
		script string
		want   string
	}{
		{"crash", "#!/bin/sh\nexit 7\n", "status 7"},
		{"partial JSON", "#!/bin/sh\nprintf '{'\n", "malformed versioned JSON"},
		{"non UTF-8 JSON", "#!/bin/sh\nprintf '\\377'\n", "non-UTF-8 JSON"},
		{"unexpected second value", "#!/bin/sh\nprintf '{}{}'\n", "multiple JSON values"},
		{"hostile diagnostic", `#!/bin/sh
printf '%s\n' '{"schemaVersion":1,"error":{"code":"bad code /tmp/private","message":"capability-body-must-not-leak"}}' >&2
exit 1
`, "read-the-code-axi failure"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Client{Binary: writeScript(t, test.script)}).Open(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "capability-body") {
				t.Fatalf("error = %v, want bounded %q without product message", err, test.want)
			}
		})
	}

	oversize := writeScript(t, "#!/bin/sh\ndd if=/dev/zero bs=1048576 count=9 2>/dev/null\n")
	if _, err := (Client{Binary: oversize}).Open(context.Background(), request); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("oversized stdout error = %v", err)
	}

	pidPath := filepath.Join(t.TempDir(), "child.pid")
	hanging := writeScript(t, fmt.Sprintf("#!/bin/sh\nsleep 20 &\nchild=$!\nprintf '%%s' \"$child\" > %q\nwait \"$child\"\n", pidPath))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := (Client{Binary: hanging}).Open(ctx, request); err == nil {
		t.Fatal("cancelled CLI process was accepted")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("CLI cancellation took %s", elapsed)
	}
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read cancelled child pid: %v", err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatalf("decode cancelled child pid: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err == nil || !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("cancelled read-the-code-axi child process %d survived: %v", pid, err)
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "read-the-code-axi")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
