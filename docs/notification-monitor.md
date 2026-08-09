# Sophon notification monitor protocol

The notification monitor is Sophon's one intentional long-lived local
process. It transports optional triggers only. Canonical files remain the
only truth; `sophon status` recovers every important fact after monitor loss.
The monitor never owns, starts, restarts, or recovers a commander or worker,
and never performs a lifecycle action.

## Transport and identity

Protocol version 1 uses JSON-RPC 2.0 over a Unix stream socket whose inode is
`<data-home>/state/monitor/rpc.sock`. The parent is mode `0700` and the socket
is mode `0600`. On systems with short Unix socket path limits, Sophon creates
a deterministic `/tmp/sophon-monitor-<data-home-hash>` symlink that resolves
to that exact private parent and uses it only as the kernel addressing path;
the socket inode remains under the data home. The server validates same-user
peer credentials on Darwin and Linux.

Every frame is a four-byte unsigned big-endian length followed by exactly one
UTF-8 JSON object. Length must be `1..16384`; partial frames time out after two
seconds. There is no newline delimiter. Requests have exactly `jsonrpc`,
`method`, `id`, and `params`; `jsonrpc` is exactly `"2.0"`, `id` is a non-empty
string of at most 64 bytes, and `params` is a method-specific object. Unknown
or extra fields, notifications without an id, numeric/null ids, trailing JSON,
and wrong versions are refused. Standard JSON-RPC errors cover parse,
envelope, params, and unknown-method failures.

`state/monitor/runtime.json` is atomically published mode `0600` and contains
the runtime record version, process id, start time, and an unguessable 256-bit
process generation. Clients read it directly and include `protocol_version: 1`
and that exact `generation` in every request. The generation never appears in
process arguments, logs, or public CLI JSON. Request ids cannot be replayed
within one monitor generation (the last 1024 are retained in bounded memory).

## Methods

All shipped calls are id-bearing requests and return a typed acknowledgement.
Acknowledgement `status` is one of `accepted`, `coalesced`, `rejected`, or
`unavailable`; it also carries `protocol_version`, `method`, and an optional
bounded diagnostic `detail`.

### `monitor.ping`

Params: `protocol_version`, `generation`.

Returns `protocol_version`, `status: "accepted"`, and the stable capability
list. This is the readiness gate used by `monitor start`; start never claims
ready before an authenticated ping succeeds.

### `monitor.status`

Params: `protocol_version`, `generation`. Returns an accepted acknowledgement.
The public `sophon monitor status --json` output is a separate redacted,
versioned shape: `protocol_version`, `running`, `status`, optional `pid` and
`started_at`, capabilities, and a bounded detail. It never includes the
generation.

### `notify.progress`

Params: `protocol_version`, `generation`, `task_id`, positive `attempt`,
`phase`, and optional `note`. Phase is exactly one of `investigating`,
`implementing`, `testing`, `waiting`, or `blocked`. The UTF-8 note is collapsed
to one printable line and capped at 256 bytes. The monitor requires the exact
current task/attempt and a canonical spawn receipt, then coalesces and forwards
fixed Sophon-generated prose to the exact attached commander. It writes no
file or replay record.

### `notify.task_changed`

Params: `protocol_version`, `generation`, `task_id`, positive `attempt`,
`change`, and `change_generation`. Change is exactly `completion`, `report`,
`verification`, `validation`, `delivery`, or `release`. `change_generation` is
the lowercase SHA-256 of that canonical record's bytes. The monitor rereads
the current task, spawn, and typed record; validates exact identity, conflict
rules, terminal delivery/release fields, and the digest; then coalesces the
trigger. Publication always happens before this request, and rejection never
rolls publication back.

### `monitor.shutdown`

Params: `protocol_version`, `generation`. The server acknowledges before
closing, flushes its bounded pending notifications, closes the listener, and
removes socket/runtime files only when they still match its exact generation
and pid. `sophon monitor stop` waits for exact absence and never signals a pid.

`notify.turn_ended` is not present in protocol version 1. Current Herdr does
not expose a supported exact-pane, explicit-session, non-polling turn-end
signal through Sophon's adapter; polling and private runtime hooks are outside
this monitor's authority.

## Bounds and lifecycle

The server accepts at most 16 concurrent clients, 128 requests per second,
128 distinct pending task/attempt notifications, and 1024 replay ids. A
175-millisecond one-shot timer coalesces each exact task/attempt burst; tasks
or attempts are never merged, and durable changes dominate optional progress.
Forwarding uses the attached commander's exact registered Herdr identity and
the running-safe submit path. Missing/stale commander delivery is dropped and
logged only as fixed metadata; there is no durable event ledger or replay
queue.

`sophon monitor run` stays in the foreground. `start` launches a detached
same-binary child for the resolved data home and converges concurrent starts
through a crash-released file lock plus authenticated socket identity. Stale
cleanup requires a dead/unreachable socket, an exact unchanged private runtime
record, and a definitively dead pid; a live/reused pid is conservative refusal.
An orphan socket from a crash between bind and runtime publication is removed
only after two exact inode observations and proof no listener accepts it.
SIGTERM and interrupt use graceful exact-instance cleanup. Background logs
are mode `0600`, rotate at 256 KiB with one backup, contain no generations or
message bodies, and foreground logs remain directly inspectable.
