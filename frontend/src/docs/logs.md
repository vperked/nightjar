# Logs

The monitor writes one tidy line per event: level, component, message, then details as `key=value`. Secrets such as tokens, passwords and webhook addresses are masked before anything is written.

```
2026-11-02 04:13:03.175  WARN   web         Failed sign-in  ip=203.0.113.9
```

## In the dashboard

**[Monitor log](/logs)** (in the account menu) shows the last 3,000 entries, following new ones live. Filter by level, component or text, and download what you see.

Above the entries, **Problems in this log** reads every warning and error the log keeps and says what they mean: a recognised cause with its fix, or a repeated error grouped with its count. The same reading explains failed [system services](/docs/system-services#what-went-wrong).

## On the machine

- **Under systemd**, the journal has it: `journalctl -u nightjar -f`. Add `-p warning` for problems only.
- **The log file**, `nightjar.log` by default (`LOG_FILE`), rotated at 10 MB (`LOG_MAX_BYTES`).

## Settings

- `LOG_LEVEL` applies at once. `DEBUG` adds every cycle's readings; at `INFO` they are logged every 15 minutes.
- `LOG_FORMAT=json` writes JSON lines, for log shippers such as Loki, Vector or Datadog.
