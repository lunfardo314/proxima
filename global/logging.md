## Logging configuration

Proxima uses a per-topic verbosity system that allows fine-grained control over log output.
Each log message belongs to a topic and has a verbosity level. A message is emitted only when
its level is at or below the configured verbosity for that topic.

### Configuration

In `proxima.yaml`:

```yaml
logger:
  # global verbosity: default level for all topics (0=essential, 1=normal, 2=verbose)
  verbosity: 0
  # directory for all log files; "" (default) means the working directory
  directory: ""
  output: proxima.log
  previous: save
  keep_latest_logs: 2
  # per-topic verbosity overrides
  topics:
    tag_along: 1
    branch_attach: 1
```

The `verbosity` value is the default for any topic not listed under `topics`.
Setting a topic to a higher level enables more detailed output for that topic only.

`directory` is where the live log, rotated logs and crash logs are written. It defaults to the
working directory. Several nodes on one machine may point at the same directory: each writes its
own `output` basename, and both rotation/purge and crash-log naming are keyed on that basename, so
nodes never touch each other's logs.

### Available topics

| Topic               | Levels | Description                                                                                                               |
|---------------------|---|---------------------------------------------------------------------------------------------------------------------------|
| `lifecycle`         | 0 | Work process start/stop messages                                                                                          |
| `tag_along`         | 0, 1 | Tag-along output processing. Level 0: permanent failures (blacklisted). Level 1: successful additions, transient failures |
| `freeze_delegation` | 1 | Delegation freeze operations during proposal building                                                                     |
| `branch_attach`     | 1 | Branch transaction attachment status (coverage, inflation, supply)                                                        |
| `seq_attach`        | 1 | Non-branch sequencer transaction attachment status                                                                        |
| `branch_commit`     | 1 | Branch state commit to the multi-state DB, orphan detection                                                               |
| `rate_control`      | 1 | `txsenders` module events of exceeding rate control bounds                                                                |
| `poker`             | 2 | Poker module purge statistics                                                                                             |

### How levels work

- Level 0 messages are emitted when global `verbosity` is 0 (the default). These are essential messages
  that indicate permanent failures or critical state changes.
- Level 1 messages provide normal operational detail: successful operations, transient errors, state transitions.
- Level 2 messages are verbose diagnostics, useful for debugging.

A topic configured with level N shows all messages at levels 0 through N for that topic.

### Code API

In Go code, use `LogTopicf` and `WarnTopicf` instead of `Infof`/`Warnf` for messages that should
respect per-topic verbosity:

```go
// emitted when tag_along topic verbosity >= 1
l.LogTopicf("tag_along", 1, "output %s has been added to '%s'", oid, seqName)

// emitted when tag_along topic verbosity >= 0 (always, unless explicitly suppressed)
l.WarnTopicf("tag_along", 0, "output blacklisted permanently, reason = '%v'", err)
```

Use `TopicVerbosityLevel(topic)` to conditionally build expensive log strings:

```go
if l.TopicVerbosityLevel("branch_attach") > 0 {
    msg += fmt.Sprintf(", slot inflation: %s, supply: %s", ...)
}
```

Direct `Infof`/`Warnf` calls bypass the topic system and are always emitted regardless of verbosity settings.

### Crash logs

On graceful shutdown with a reason (`GracefulShutdown`) and on any fatal exit (assertion failures,
`.Fatalf` — via a zap fatal hook), the current log file is flushed and copied to
`crash-<output basename>.<unix time>` next to it, capturing the reason and preceding history before
the next startup rotates/purges the live log. The `output` basename in the name keeps the crash logs
of several nodes sharing one directory distinct. Files whose basename starts with `crash` are never
auto-cleaned by the startup log purge (`keep_latest_logs` applies only to rotated
`<output>.<timestamp>` files); remove crash logs manually once no longer needed.
