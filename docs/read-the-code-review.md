# Read the Code review integration

Sophon integrates with the standalone public `read-the-code-axi` executable
through its documented schema-version-1 JSON CLI. Sophon does not import Read
the Code modules, inspect its state directory, scrape its browser UI, or copy
its session protocol. Read the Code owns browser capabilities, session
persistence, diff anchors, and submission sequence. Sophon's filesystem owns
task posture, exact task/attempt bindings, ingested review evidence,
classification, correction routing, approval eligibility, and delivery.

The package is not published yet. Install or pack Read the Code separately and
configure the exact executable with `--read-the-code PATH` on each relevant
command or `SOPHON_READ_THE_CODE=PATH`. The flag wins over the environment.
Sophon never downloads a package, contacts a registry, searches a repository
for a binary, or assumes a global installation.

## Task posture and commands

Task intake accepts `--review off|optional|required`; old tasks and an omitted
flag are `off`.

- `off` refuses a review binding and adds no review delivery gate.
- `optional` permits an explicit local review but does not gate delivery.
- `required` adds `open-review` after verification and configured validation,
  and blocks every delivery until the current exact head is approved.

An explicit operator decision can monotonically escalate posture while
preserving immutable history:

```text
sophon review set TASK --posture optional|required
```

The review lifecycle is:

```text
sophon review open TASK [--attempt N] [--no-browser] [--json]
sophon review status TASK [--json]
sophon review feedback TASK [--attempt N] [--after N] [--limit N] [--json]
sophon review classify TASK --sequence N \
  --disposition requested-changes|non-actionable
sophon review apply TASK --sequence N
sophon review acknowledge TASK --sequence N
sophon review reconcile TASK [--json]
sophon review end TASK [--json]
```

`open` requires the exact current clean worker repository, canonical verified
base/head, and a passing current-head validation receipt when validation is
configured. It invokes `read-the-code-axi open` directly without a shell and
accepts only the expected session, revision, status, schema, and loopback
capability URL. Schema-version-1 additive fields are ignored as the public
product contract requires; required identity fields and every known event are
still checked exactly. The canonical `review/open.json` omits the URL, repository
path, executable path, and process data. Human `open` launches the browser;
`--no-browser` returns its URL only to that invoking local operator. `--json`
is the sole machine output containing it.

`feedback` is the only command that returns comment bodies. Its output is
bounded and labels comments as untrusted product data. A commander must read
each feedback submission, classify it against accepted task intent, and mark
test/non-actionable feedback explicitly. `apply` routes only a fixed pointer
containing task, attempt, and sequence through exact worker steering; it never
puts arbitrary comment bodies in a Herdr/process argument. The worker reads
the canonical bounded feedback command and treats the bodies as data, not
instructions or authority.

## Durable records and bridge

Each attempt may contain:

```text
attempts/<n>/review/
  open.json
  events/<20-digit-sequence>.json
  decisions/<20-digit-feedback-sequence>.json
  routes/<20-digit-feedback-sequence>.json
  approval-acknowledgements/<20-digit-approval-sequence>.json
```

All records are typed, private, atomic, and immutable. Event filenames must be
a contiguous sequence beginning at one; cursor is derived from those files,
not stored as a mutable offset. Duplicate identical product submissions are
idempotent. A gap, conflict, replay with different bytes, wrong session,
wrong task/attempt/base/head, unknown type/schema, bad anchor/path, control
text, or exceeded bound stops ingestion without skipping or advancing.
`status --all` exposes bounded review history while retaining every old record.

Opening or reconciling starts one narrow per-revision bridge. A crash-released
kernel file lock is the owner; its private JSON companion is inspectable
identity only, and PID is never liveness authority. The bridge does exactly:

1. derive the canonical cursor;
2. run one bounded blocking `read-the-code-axi poll --after CURSOR` directly;
3. validate and atomically publish every contiguous event;
4. send one fixed `notify.task_changed` review notification; and
5. repeat until exact ownership becomes stale, the review ends, or the task is
   delivered/released.

Manual reconciliation uses the same lock and never becomes a concurrent poll
consumer. The bridge has no ledger, retry database, task transition, browser
capability, or commander ownership. The existing JSON-RPC monitor still only
validates/coalesces a digest of the latest canonical immutable event and
forwards fixed prose. Missing monitor/bridge processes lose latency only:
`review reconcile` resumes from the derived cursor, and Read the Code events
are non-destructive.

## Delivery gate

Immediately before any required-review delivery effect Sophon rechecks the
external product's non-capability status and canonical records. Delivery
requires all of:

- the current verified head and configured passing validation;
- a current attempt binding matching the exact spawn base and outcome head;
- a canonical approval event for that exact head, later than every feedback
  event;
- every feedback submission classified, with no requested change remaining;
- no end, event gap, invalid evidence, stale product revision/approval, or
  product/canonical cursor drift; and
- a fresh public-surface preflight plus a separate `--confirmed` operator
  delivery invocation.

Approval never confirms delivery and grants no push, PR, merge, destructive,
or unrelated authority. Any new task attempt/head has its own binding and
starts with no approval; old sessions, comments, and approval remain history.
Ending a review is an explicit operator command and never erases evidence.
