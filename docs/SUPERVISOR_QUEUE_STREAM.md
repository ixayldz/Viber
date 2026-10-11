# Supervisor goal queue and provider stream contract

Viber exposes two distinct kinds of queued work. `/queue add TEXT` (or the
existing `queue` CLI command) stores future steering input for the current task.
`/goal add CHILD_TASK_ID TEXT` or `goal-queue --command-file FILE` records a
durable request for a separate child task. A goal is created from the source snapshot and
configuration revision bound when it was queued; it does not become input to
the parent task. Activating a goal is explicit. Only a still-queued goal can be
cancelled.

The durable goal lifecycle is `QUEUED` → `ACTIVATING` → `ACTIVATED`, with
`CANCELLED` available from `QUEUED`. The activation intent is persisted before
child creation. The owner reconciles pending intent after restart using the
stable child task ID, validates the immutable parent, snapshot and goal
bindings, and stores a child activation receipt before dispatch. A child that
has advanced past its recorded activation state is not replayed automatically;
the operator must inspect and recover it. `goal-queue list` shows each
schedule's state. `goal-queue activate GOAL_ID` starts or schedules it, and
`goal-queue recover GOAL_ID` retries safe reconciliation.

Attachment has a durable task-event cursor. Provider output has an independent,
ephemeral cursor. For example:

```text
viber attach TASK --store STORE --after 12 --provider-deltas --provider-after 4 --follow
viber goal-queue TASK --store STORE --json
viber goal-queue TASK --store STORE --command-file FILE
```

An attachment page carries `current_state_ref` (`task_id`, `task_seq`, and
`document_digest`), the earliest retained task event, and the active invocation
when present. A cursor older than retained history returns an
`attachment_gap` envelope with the current state reference and earliest
available task sequence. Viber exits with status 3 instead of silently
advancing that cursor. Passing `--resync` explicitly skips to the current
durable state sequence and emits an `attachment_resynced` envelope first.

With `--provider-deltas`, each `provider_text_delta` JSONL envelope contains a
stream-local `delta_seq`, its independent `durable_task_seq` reference, the
invocation ID, and the decoded visible text. The stream publishes only complete
visible assistant text records from a valid provider stream. Tool arguments,
reasoning, signatures, and malformed or incomplete protocol events are not
published. These deltas are not journal events or a transcript; they exist only
in the live owner's bounded ring (at most 128 records and 64 KiB per task), so
a slow reader cannot block inference. After owner restart, prior deltas report
`NOT_RETAINED`.

When old deltas have been evicted, a `provider_stream_gap` envelope reports the
requested cursor, earliest retained delta, current delta sequence, and current
durable state reference. Viber exits with status 3 unless `--resync` was
provided. Resync emits `provider_stream_resynced` and moves the provider cursor
to the current delta head, explicitly skipping unavailable text. The task
cursor (`--after`) and provider cursor (`--provider-after`) never substitute
for one another.

The local owner protocol uses `attach-page` with `owner.Page{After, Limit}` and
`stream-page` with `owner.StreamPageQuery{After, Limit, WaitMillis}`. Stream
waits are bounded to one second; readers can poll independently. If no live
owner retains the stream, the response is `NOT_RETAINED` with the current
durable state reference. No provider API key is required to exercise these
contracts; the tests use local HTTP fixture streams.

## Focused acceptance

Run from the repository root with the workspace Go toolchain and caches:

```powershell
$env:GOCACHE = (Resolve-Path '.cache/go-build').Path
$env:GOMODCACHE = (Resolve-Path '.cache/go-mod').Path
$env:GOMAXPROCS = '2'
$env:GOTOOLCHAIN = 'local'
.tools/go1.27.2/go/bin/go.exe test -p 1 ./internal/model ./internal/owner ./internal/agent
```

The focused acceptance cases are `TestStreamingLocalHTTPWaitsForTerminalAndCancellationRetainsRawPrefix`,
`TestProviderStreamBoundsSlowReaderAndRequiresResync`,
`TestDetachedInvocationOutlivesClientAndSupportsCursorReconnect`, and
`TestGoalQueueCreatesSeparateSnapshotBoundChildAndRecoversAfterRestore`.
Windows may require running the owner tests in the approved local environment
because they create ACL-protected owner endpoints and stores.
