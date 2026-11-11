# Nightjar audit — 2026-11-05

Scope: the current working tree (including uncommitted changes) of `backend/` (Go) and `frontend/src/` (React/TS),
plus the Dockerfile, Makefile and deploy scripts.

**Method.** 14 auditors each read one slice end to end: 9 backend subsystems, 3 frontend areas, a check of every
frontend call and type against the Go routes and structs, and a repo-wide security sweep of exec, file writes,
outbound URLs, unauthenticated routes and secrets. An independent verifier then tried to refute every finding by
reading the code. Each high-severity finding got a second skeptic, and a completeness critic looked for gaps.
146 raw findings → 5 refuted → 141 survived (126 confirmed, 15 plausible) + 6 from the critic. After merging
findings that several auditors reported, there are **130 unique issues: 7 high, 35 medium, 88 low, no critical.**

**Baseline (all clean):** `go vet ./...`, `go test ./...`, `go test -race`, `tsc -b`, `vitest` (112 tests).
Note: no package under `backend/internal` has in-package tests (all tests live in `backend/test`), so nothing
covers unexported paths like `edacErrors`. That is how the panic below got through.

## Status as of 2026-11-09

Every finding was checked again against the code on 2026-11-09, and each one now has a **Status** line:
**129 fixed** and **1 accepted** (#57: see its entry), with none still open. All 7 high and all 35 medium findings are fixed.

To keep this useful, give each new finding a Status line, and update the line when the code changes.

## Fix first

These are the issues with the worst impact per line of fix. All were confirmed by two independent reviewers.

1. **Four dashboard buttons have never worked in production** ([client.ts:133](frontend/src/api/client.ts#L133)).
   `request()` sets `Content-Type` only when there is a body, and `fromDashboard` ([writes.go:27](backend/internal/api/writes.go#L27))
   answers any non-DELETE request without `application/json` with 415. Affected: *Repair now*, *Check now* on media cleanup,
   *Keep* on media cleanup, and *Send report now*. The worst is *Keep*: the owner thinks the title is protected,
   but the next cleanup pass deletes it. Fix: always send the header for non-GET requests, or let empty bodies through on the server.
2. **The kernel check panics on any host without EDAC sysfs** (most VMs, cloud instances and ARM boards). See
   [kernel/monitor.go:420](backend/internal/kernel/monitor.go#L420): `d.Name()` is called before `err` is checked, and WalkDir passes a nil
   `d` when the root is missing. `safeCycle` recovers, but the rest of that cycle is skipped, so updates, uptime checks, Plex, units,
   SMART and notifications don't run, and crash/reboot alerts are lost. The fix is one line: check `err != nil` first.
3. **A "Without secrets" backup still contains secrets** ([backup.go:52](backend/internal/api/backup.go#L52)). The `remote.hosts` and
   `plugins.state` meta blobs are copied unfiltered. They include each agent's bearer token, and that token is enough to push a new
   binary to `/v1/update`, which means root on every monitored server. Strip `Token` and plugin secret settings when `withSecrets` is false.
4. **Disk cleanup does not check which disk is full** ([cleanup/disk.go:362](backend/internal/cleanup/disk.go#L362),
   [monitor/disk.go:270](backend/internal/monitor/disk.go#L270)). A media disk at 96% (normal on a Plex box) sets off
   `EmergencyClean`, which truncates every `/var/log` file, deletes rotated logs, wipes `/tmp` and restarts log daemons
   every 5 minutes. It also sends a Critical alert each time, and it frees nothing on the full disk. Only clean when the full mount is the one holding
   `/var/log`, `/tmp` or the journal.
5. **Media cleanup can delete pack/collection torrents** ([mediacleanup.go:351-355](backend/internal/mediacleanup/mediacleanup.go#L351)).
   When the *arr history lookup fails (the error is discarded) or there is no *arr at all, the code falls back to
   title+year substring matching. "Alien (1979)" then matches "Alien Quadrilogy 1979-1997", which is deleted with its files.
   Never fall back to fuzzy matching for a `deleteFiles=true` removal. At minimum, propagate the Downloads error and require an exact single-title match.
6. **Rotated-log removal and truncation follow symlinked directories as root** ([cleanup/logs.go:169](backend/internal/cleanup/logs.go#L169)).
   Writable subdirectories of `/var/log` owned by service users allow a TOCTOU attack that ends in arbitrary root file deletion or truncation.
   A related issue is `SetupNginxLogs`, which chowns through symlinks (the CVE-2016-1247 pattern, [disk.go:449](backend/internal/cleanup/disk.go#L449)).
   Use `openat`-style traversal (`os.Root` in Go 1.24+) rooted at `/var/log`.
7. **Repair scripts that background a process hang forever** ([repair/repair.go:189](backend/internal/repair/repair.go#L189)).
   The child keeps the stdout pipe open, so `cmd.Run` never returns. The check is stuck as "running" and shutdown blocks in `loop.Wait()`.
   Set `cmd.WaitDelay` and `SysProcAttr.Setpgid` and kill the process group. The same bug class is in update execution
   ([updates/exec.go:95](backend/internal/updates/exec.go#L95)).

Next, the medium issues most likely to be hit:

- **Long actions fail in the UI even though they succeed.** The client has a 15 s timeout ([client.ts:318](frontend/src/api/client.ts#L318)),
  and its abort cancels the server's context. The server's `WriteTimeout` is 30 s ([server.go:253](backend/internal/api/server.go#L253)).
  SSH reinstalls (5 min), unit restarts (90 s), cleanup scans and plugin trials all show errors, and some are killed partway through.
- **Login throttle bypass via `X-Real-IP` behind a proxy** ([server.go:347](backend/internal/api/server.go#L347)). A spoofed IP lets an
  attacker brute-force passwords or deliberately lock out the owner. A related issue: `WEB_AUTH_TOKEN` can be guessed with no throttle through
  the public `/api/config` and `/health` ([server.go:457](backend/internal/api/server.go#L457), found by the critic).
- **Data race: `/api/stats` mutates the process scanner's shared cache** ([server.go:627](backend/internal/api/server.go#L627)). Alerts
  lose command lines and RSS. The `-race` tests do not exercise this path.
- **Every TCP connection to sshd is treated as an SSH login** by the DDoS defender ([ddos/read.go:141](backend/internal/ddos/read.go#L141)).
  An attacker that connects is whitelisted, and the dashboard refuses rules that would block it.
- **Setup's Plex step deletes the saved token** if it is not retyped ([Setup.tsx:144](frontend/src/pages/Setup.tsx#L144)). In
  **Maintenance**, editing a second window saves over the first one ([Maintenance.tsx:200](frontend/src/pages/Maintenance.tsx#L200)).
- **`LOG_FORMAT=json` is always rejected** ([config/fields.go:175](backend/internal/config/fields.go#L175)), and **`INSTALL_SYSTEMD=true`
  makes the service crash-loop** ([main.go:100](backend/cmd/nightjar/main.go#L100)).
- **Secrets leak into logs and notification history.** Telegram bot tokens and Slack or webhook URLs appear in delivery errors
  ([notify/hub.go:361](backend/internal/notify/hub.go#L361), [logx.go:16](backend/internal/logx/logx.go#L16)), and the heartbeat secret
  appears in every Discord alert ([monitor/services.go:136](backend/internal/monitor/services.go#L136)).

## What the audit did not cover well

From the completeness critic:

- No slice named `internal/selfcheck`, `internal/setup`, `internal/web` or `internal/metrics`.
- These high-risk files got zero findings: `internal/firewall/*`, `internal/apps/manager.go`, and `internal/services/checks.go` / `slots.go`. The critic re-read
  firewall and checks and found them sound; apps produced one new low finding.
- Little attention went to `api/statuspage.go`, `appearance.go`, `heartbeats.go`, `logs.go`, `monitor/botactions.go` (Discord custom_id
  truncation) and the `sanitizeHTML` half of `customCode.ts`. The critic reviewed `sanitizeHTML` for mXSS and found nothing.
- Backup **restore** writes every meta blob without the validation the normal endpoints apply. It is admin-only, so it is not filed as a defect.

## Refuted during verification (not issues)

- `/health` hanging on a stale network mount: gopsutil `Partitions(false)` doesn't stat the mount.
- The tokenless "commands locked" bypass in updates: unreachable, because `Accounts` is always set.
- Kernel flood detection never ending: the rate math does decay.
- StatusPage duplicate React keys: the rows have no state, so this is harmless.
- ServiceDetail keeping its delete-confirm armed across checks: the page remounts by pathname.

## All findings (130 unique, after merging duplicates found by more than one auditor)

High: 7 · Medium: 35 · Low: 88 · Critical: 0


### High

#### 1. "Without secrets" backup still contains agent tokens (remote root) and plugin secret settings

`x-security-1` (also reported as storage-config-main-1, x-contract-2, api-auth-7) · security · verifier: **confirmed** · [backend/internal/api/backup.go:22](backend/internal/api/backup.go#L22)

- **Status (2026-11-09):** ✅ fixed. Backup.go withoutSecrets strips agent tokens and secret plugin settings; keepSecrets restores them.
- **What:** makeBackup removes only config.Settings fields of KindSecret when secrets=0. It then copies the meta keys in backupKeys verbatim, and those include "remote.hosts" and "plugins.state". remote.Host stores the agent bearer token as `Token string json:"token,omitempty"` (remote/remote.go:53), so every agent token goes into the file. plugins.State.Settings holds every plugin setting, including ones of type "secret". An agent token is root on the remote server: the holder can POST /v1/update to the agent with any ELF build, and the agent swaps itself for that build and restarts (agent/update.go). The holder can also rewrite the agent's firewall rules. The UI calls this download "Without secrets" and the "With secrets" button carries the warning to keep the file safe, so users will treat the plain backup as safe to share.
- **When it bites:** The admin clicks Settings > Backup > "Without secrets" and attaches nightjar-backup-2026-11-05.json to a GitHub issue or support chat. Anyone who reads it gets meta["remote.hosts"][i].token for each server. With that token, they derive the agent's pinned TLS key (it comes from the token), connect to https://host:9101/v1/update, and push their own binary with a matching X-Agent-Build hash. That gives root code execution on every monitored server. Secret plugin settings (API keys, passwords) leak the same way.
- **Fix:** When withSecrets is false, clear secret material from the meta values before encoding: zero Host.Token in remote.hosts, and drop plugin settings whose manifest type is "secret". On restore, keep the existing token or secret when the incoming one is empty, as saveHost and plugin Configure already do.

#### 2. Disk cleanup ignores the full mount and empties every /var/log file when an unrelated partition (or the total across disks) is full

`host-collect-3` (also reported as monitor-core-1) · data-safety · verifier: **confirmed** · [backend/internal/cleanup/disk.go:362](backend/internal/cleanup/disk.go#L362)

- **Status (2026-11-09):** ✅ fixed. Cleanup/disk.go cleans only on the same filesystem; trigger uses root/log mount percent.
- **What:** EmergencyClean(mount) and CleanDiskSpace(mount, ...) use `mount` only to measure usage. What they clean is always /var/log (every log truncated to 0 in aggressive or emergency mode, all rotated archives deleted), /tmp, /var/tmp and /var/cache, and they restart log services and nginx, whether or not those paths are on that mount. The monitor calls them per partition (monitor/disk.go:270,290 with p.Mountpoint, MONITOR_INDIVIDUAL_PARTITIONS defaults to true). diskCleanup.check also calls CleanLogsAndRestart and EmergencyClean("/") based on s.DiskPercent, which is TotalDisk.Percent across all disks (resources.go:63).
- **When it bites:** A media server's /mnt/media data disk is 96% full, which is normal. Every 5 minutes (diskEmergencyCooldown), EmergencyClean("/mnt/media") truncates syslog, kern.log, nginx and app logs to zero, deletes all rotated logs, wipes /tmp files older than 1h, restarts rsyslog and nginx, and sends a Critical 'Emergency' Discord alert (BytesCleared > 0 because the logs refill). None of this frees a byte on /mnt/media. Forensic logs are destroyed continuously, and the same happens when one big disk pushes the aggregate total past 85% or 95%.
- **Fix:** Only clean targets on the same filesystem as `mount`: compare st_dev of each candidate dir or log with that of the mount, and skip cleanup entirely when none match. Trigger root cleanup from the root partition's percent, not TotalDisk.Percent.

#### 3. Rotated-log removal and log truncation follow symlinked intermediate directories (TOCTOU), allowing arbitrary root file deletion or truncation

`host-collect-2` · security · verifier: **confirmed** · [backend/internal/cleanup/logs.go:169](backend/internal/cleanup/logs.go#L169)

- **Status (2026-11-09):** ✅ fixed. Cleanup/logs.go goes through os.OpenRoot(LogDir); looksLikeLog rejects .so/.ko names.
- **What:** RotatedLogs()/AllLogFiles() walk /var/log and collect full path strings. Later, CleanupRotated (os.Remove(info.Path)), Clean (os.Remove at line 292) and cleanRegular (openRegular, O_NOFOLLOW only on the last component, line 305) act on those strings. Every directory component is re-resolved at that time, so a directory swapped for a symlink after the walk sends the unlink or truncate anywhere. CleanTree uses os.Root for exactly this reason, but log cleanup does not. Directories under /var/log are often writable by non-root accounts: on this host /var/log itself is root:syslog 0775, /var/log/unpackerr is owned by unpackerr (which unpacks untrusted downloads) and /var/log/tor by debian-tor. The rotated-log regex also matches shared libraries (`libc.so.6` ends in `.6`) and kernel modules (`*.ko.zst`).
- **When it bites:** A compromised unpackerr process creates /var/log/unpackerr/x/libc.so.6 and backdates its mtime past ROTATED_LOG_RETENTION_DAYS. It waits with inotify until nightjar has listed x, then renames x away and replaces it with a symlink to /usr/lib/x86_64-linux-gnu. The routine CleanupRotated pass, which runs every LOG_CHECK_INTERVAL with no disk pressure needed, unlinks the real libc as root and the host can no longer run anything. In aggressive mode, the same trick truncates root-owned files with 'log' or 'auth' in their name, such as /etc/pam.d/common-auth or /etc/login.defs.
- **Fix:** Do all log deletes and opens relative to an os.OpenRoot("/var/log") handle (Root.Remove / Root.OpenFile), as CleanTree already does. Alternatively, open each parent with O_NOFOLLOW|O_DIRECTORY and use unlinkat/openat. Also limit rotated-name matching to files whose base name looks like a log.

#### 4. edacErrors dereferences a nil DirEntry when the EDAC sysfs root is missing, so the monitoring cycle panics

`host-collect-1` (also reported as monitor-core-2) · correctness · verifier: **confirmed** · [backend/internal/kernel/monitor.go:421](backend/internal/kernel/monitor.go#L421)

- **Status (2026-11-09):** ✅ fixed. Kernel/monitor.go EdacCounts uses os.ReadDir, no WalkDir callback.
- **What:** filepath.WalkDir calls fn(root, nil, err) when Lstat of the root fails (confirmed in GOROOT path.go:397-400). The callback runs `name := d.Name()` before it checks `err`, so d is a nil interface and the call panics. The walk starts at "/sys/devices/system/edac/mc", which does not exist on kernels without EDAC (CONFIG_EDAC=n, many ARM/RPi kernels, some VMs and containers). hardwareErrors() -> CheckCrashes() therefore panics every time on such hosts.
- **When it bites:** On a host without /sys/devices/system/edac/mc, every KERNEL_CHECK_INTERVAL cycles (default 30) checkCrashes panics. Loop.safeCycle recovers it, but the rest of Evaluate is skipped: updates, services, plex, units, drives, Notifications.Tick, analytics and setLatest. An error report goes out every time, and no kernel crash or reboot alert can ever be delivered, because the CrashReport is never returned.
- **Fix:** Check err (and d == nil) before using d: `if err != nil || d == nil { return nil }`, then read d.Name(). Better still, os.Stat the root first and skip it when it is missing.

#### 5. Media cleanup matches torrents by title+year and deletes them with their files, which can wipe collection or pack torrents holding other watched movies

`integrations-1` · data-safety · verifier: **confirmed** · [backend/internal/mediacleanup/mediacleanup.go:355](backend/internal/mediacleanup/mediacleanup.go#L355)

- **Status (2026-11-09):** ✅ fixed. Mediacleanup.go skips items on a Downloads error; name-matched torrents keep their files.
- **What:** When an item has no download hashes, the cleanup picks torrents with sameTitle(tr.Name, it.Title, it.Year). That happens for Plex-managed items, for items whose *arr history has aged out, and also when c.Downloads fails, because its error is thrown away at line 351. sameTitle only checks that the title appears as a run of whole words and the year appears somewhere in the torrent name. remove() (line 440) then calls QB.Delete(hash, true), which deletes the torrent's files. A pack or collection torrent whose name includes the item's title and its year matches, and the whole pack is deleted from disk.
- **When it bites:** Plex library points at the qBittorrent download folder (no Radarr). The unwatched movie is "Alien" (1979) and the torrent is "Alien Quadrilogy 1979-1997 1080p". After normalising, the name contains " alien " and " 1979 ", so the pack is attached to Alien. When Alien is due, the torrent is deleted with deleteFiles=true, and Aliens, Alien 3 and Resurrection (possibly watched) are removed from disk and from Plex. The same thing happens for a Radarr-managed movie if Radarr's /history call times out once, because hashes is then empty.
- **Fix:** Don't delete files of torrents matched only by name. Either drop the title fallback for deletion or make it delete the torrent without files. Also require that the torrent's content path is the item's own folder or file (compare SavePath/content_path with it.files). Treat a Downloads error as "unknown" and skip the item instead of falling back to title matching.

#### 6. Repair script timeout cannot fire when the script leaves a background child; repair and shutdown hang forever

`updates-apps-1` · resource-leak · verifier: **confirmed** · [backend/internal/repair/repair.go:189](backend/internal/repair/repair.go#L189)

- **Status (2026-11-09):** ✅ fixed. Repair/repair.go output to an os.Pipe file, Setpgid, kills the group, 1s drain cap.
- **What:** runScript uses exec.CommandContext with Stdout/Stderr set to a bytes.Buffer, so Go makes a pipe and cmd.Run waits until every holder of the write end closes it. WaitDelay is not set and the process group is not killed. When scriptTimeout runs out, only the direct child is SIGKILLed. Any grandchild that inherited stdout (for example `nohup ./server &` or `app &`, the usual way a repair script restarts a non-systemd process) keeps the pipe open, so Run never returns. The caller in monitor/services.go runs repair inside the serviceChecks WaitGroup, and the outage's `running` flag is only cleared after Run returns.
- **When it bites:** A repair script for a non-systemd app runs `cd /opt/app && nohup ./app &` and exits 0. The daemon holds the stdout pipe, so cmd.Run blocks for as long as the daemon lives. (1) o.running stays true, so Due never allows another attempt and no 'repaired' notice is sent. (2) serviceChecks.running.Add(1) is never matched by Done, so the next settings-save restart or SIGTERM blocks at loop.Wait() -> l.services.wait() forever. The in-process exec restart never happens, and the API is already shut down, so the monitor is dead until systemd kills it. The dashboard 'repair now' request (api/heartbeats.go) also hangs.
- **Fix:** Set cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}, set cmd.Cancel to kill -pgid, and set cmd.WaitDelay (a few seconds) so Run returns after the timeout even when descendants keep the pipes open. Alternatively, give the child an *os.File (a temp file) for output instead of a pipe.

#### 7. Bodiless POST/PUT calls are always rejected with 415: Keep (cleanup), Check now, Send report and Try repair never work

`x-contract-1` (also reported as fe-core-1) · api-contract · verifier: **confirmed** · [frontend/src/api/client.ts:337](frontend/src/api/client.ts#L337)

- **Status (2026-11-09):** ✅ fixed. Client.ts request() sends a {} body and JSON Content-Type for writes.
- **What:** request() sets Content-Type: application/json only when init.body !== undefined (client.ts:133). Four endpoints are called with no body: sendReport (POST /api/report/send, line 211), repairCheck (POST /api/checks/{name}/repair, line 213), cleanupScan (POST /api/cleanup/scan, line 336) and cleanupKeep with keep=true (PUT /api/cleanup/{key}/keep, line 337). All of them are behind s.writable -> fromDashboard (backend/internal/api/writes.go:27), which refuses every non-DELETE request whose Content-Type is not application/json, answering 415 "Send JSON". The Go tests miss this because their helper always sets Content-Type, and demo mode (demo.ts) skips the check.
- **When it bites:** A title is listed as 'leaving' in Plex -> Cleanup. The owner clicks Keep, and the browser sends PUT /api/cleanup/<key>/keep with no Content-Type. The server answers 415 'Send JSON', the UI shows 'Could not change it', and the title is never kept, so the next cleanup pass deletes the media through Sonarr, Radarr or Plex. 'Check now' on the Cleanup page, 'Send now' on the weekly report, and 'Try the repair' on a check fail in the same way, every time.
- **Fix:** Have request() always send Content-Type: application/json and a body ('{}') for non-GET/DELETE methods, or pass body: {} in these four calls. Add a Go test that calls these routes without a Content-Type header, the way the browser does.


### Medium

#### 8. INSTALL_SYSTEMD=true in .env makes the installed service crash-loop instead of monitoring

`storage-config-main-8` · correctness · verifier: **confirmed** · [backend/cmd/nightjar/main.go:100](backend/cmd/nightjar/main.go#L100)

- **Status (2026-11-09):** ✅ fixed. Main.go ignores INSTALL_SYSTEMD under systemd.
- **What:** main checks `config.Bool("INSTALL_SYSTEMD", false)` on every start and then calls os.Exit(install(...)). env.recommended:167-171 tells users to "Set to true to install as systemd service on startup". The unit that gets installed loads the same file via EnvironmentFile (systemd/service.go:49), and LoadDotenv reads it too, so every start under systemd sees INSTALL_SYSTEMD=true. Install returns "already exists (use --force)" because the unit file exists, so the process exits 1. With SYSTEMD_FORCE_INSTALL it rewrites the unit and exits 0. Neither path ever reaches run().
- **When it bites:** A user follows the env.recommended comment, sets INSTALL_SYSTEMD=true and starts ./bin/nightjar once as root; the unit is installed. On boot, systemd starts nightjar, which tries to install again and exits 1, repeating every 10s (Restart=always, RestartSec=10). After 5 attempts in 300s (StartLimitBurst) the unit goes to failed. The host is left unmonitored, with no alert, since the monitor never starts.
- **Fix:** Accept only the -install-systemd flag, not the environment variable. Or ignore INSTALL_SYSTEMD when running under systemd (INVOCATION_ID or JOURNAL_STREAM set) or when the unit already exists, and continue into run(). Fix the env.recommended wording to match.

#### 9. Notify plugins added or rescanned after startup never receive alerts, though the UI shows them as working alert channels

`storage-config-main-5` · correctness · verifier: **confirmed** · [backend/cmd/nightjar/main.go:237](backend/cmd/nightjar/main.go#L237)

- **Status (2026-11-09):** ✅ fixed. Plugins.go Scan calls NotifyChanged; restart picks up notify plugins.
- **What:** connectWebhooks builds the hub's destinations once, at startup, from pl.Providing(plugins.Notify), and notify.Hub has no way to add a destination later. The dashboard's "Look again" (api/plugins.go:31 → Scan) picks up new plugin folders, and Configure then turns them on. Plugins.tsx:118 even says "Choose what it hears about under Notifications." But no hub destination exists for the plugin, so nothing is ever sent to it until the process restarts. Configure does not trigger a restart.
- **When it bites:** The owner drops examples/plugins/alert-log into the plugins folder, presses Look again, fills in FILE and turns it on. The toast confirms it as an alert channel. Alerts keep flowing to the other channels, but alerts.jsonl stays empty and the plugin's "last run" never updates, with no error anywhere. A paging or alerting integration built as a plugin silently misses incidents until the next restart.
- **Fix:** Make the hub resolve plugin channels dynamically, e.g. one destination that calls pl.Providing(Notify) at send time. Or have Scan/Configure of a notify plugin request a restart, as settings changes do, and tell the user.

#### 10. Firewall and DDoS-defense state read with db.Meta: an unavailable or erroring DB is treated as "no rules" and "defense on", which strips the firewall rules from the kernel

`storage-config-main-6` · data-safety · verifier: **plausible** · [backend/cmd/nightjar/main.go:353](backend/cmd/nightjar/main.go#L353)

- **Status (2026-11-09):** ✅ fixed. Main.go leaves the firewall unmanaged on a MetaValue error; busy_timeout set.
- **What:** storage.Open returns a permanently unavailable Store on any open or schema error, with no busy_timeout and no retry. Store.Meta returns "" both for "unset" and for any error (it ignores the Scan error). main wires `Load: func() []byte { return []byte(db.Meta(firewallKey)) }` and `On: db.Meta(ddos.DefendKey) != "off"`. firewall.startLocked sees empty data, leaves m.rules empty and m.def "", and applyLocked then runs `iptables -D INPUT -j <chain>`, `-F` and `-X`, actively removing the owner's persisted block rules and default-drop policy. The defender, meanwhile, is forced on even if the owner switched it off. plugins.New and the other Meta-backed state have the same conflation.
- **When it bites:** Nightjar restarts while the database cannot be opened or read: the disk is full (the case this tool is built to survive) so WAL recovery or -shm creation fails, the file has a permission problem, or another process holds an exclusive lock. The log shows "Database unavailable", but startup carries on. The firewall manager removes the Nightjar chain from INPUT/OUTPUT and flushes it, so ports the owner blocked (or the default-drop policy) are open until the DB works and Nightjar restarts. DDoS defense starts blocking addresses though the owner turned it off.
- **Fix:** Give Meta a variant that returns (value, error), and have the firewall and defender keep their current kernel state and skip apply when the store is unavailable or the read failed. Also set `_pragma=busy_timeout(5000)` on the DSN, and consider failing startup (or retrying) when storage.Open fails, instead of running without the DB.

#### 11. Reinstall rewrites /etc/nightjar-agent.env, silently dropping AGENT_TRUSTED / AGENT_DEFEND and re-opening the firewall the admin declined

`remote-agent-2` · data-safety · verifier: **confirmed** · [backend/internal/agent/install.go:150](backend/internal/agent/install.go#L150)

- **Status (2026-11-09):** ✅ fixed. Agent/install.go keeps unknown settings; Reinstall honours AGENT_FIREWALL/AGENT_SMART=off.
- **What:** A reinstall (token == "") reads only AGENT_LISTEN/TOKEN/TLS/FIREWALL from the saved env (parseSettings, lines 236-255) and then overwrites EnvPath with Env(listen, token, plain, firewall), which writes only those four keys. The docs (frontend/src/docs/attacks.md:77,79) tell admins to put AGENT_TRUSTED (addresses never to block) and AGENT_DEFEND=off in /etc/nightjar-agent.env, and agent.go:135 and firewall.go:41 read them from the service environment. The dashboard's SSH reinstall runs `"$f" install` with no flags (ssh.go:79-80), so the -firewall and -smart defaults (true) apply. An agent first installed with -firewall=false therefore gets its port opened to everyone in ufw/firewalld/iptables (OpenPort, setup.go:145) on reinstall. AGENT_FIREWALL is also lost if OpenPort now returns "", so a later uninstall no longer closes the port.
- **When it bites:** The admin installed the agent with -firewall=false and allowed port 9101 only from the dashboard's IP, and added `AGENT_TRUSTED=203.0.113.0/24` (their office or monitoring network) and/or `AGENT_DEFEND=off` to /etc/nightjar-agent.env. They click "Reinstall agent" in the dashboard. Afterwards the env file holds only LISTEN/TOKEN/TLS: defense comes back on (if no defense state file exists) and can block the office network, and `ufw allow 9101/tcp` now exposes the agent port to the internet.
- **Fix:** On reinstall, keep every unknown KEY=VALUE line from the existing env file (rewrite only the keys install owns). Record the original -firewall/-smart choices in the env file (for example AGENT_FIREWALL=none) and honour them on reinstall instead of the flag defaults.

#### 12. http.Server WriteTimeout of 30s is shorter than several handlers' own budgets (SSH up to 5 min, unit control 90s, cleanup 5 min, repair or plugin 2 min), so finished actions return no response

`api-auth-3` (also reported as x-contract-3, fe-core-2, remote-agent-1) · correctness · verifier: **confirmed** · [backend/internal/api/server.go:253](backend/internal/api/server.go#L253)

- **Status (2026-11-09):** ✅ fixed. Api/server.go slow() extends write deadlines on long actions.
- **What:** WriteTimeout sets the connection's write deadline 30s after the request is read. runHostSSH (reinstall-agent and other actions run with a 5 minute ssh timeout), controlUnit (90s context), cleanupScan (5 min), repairCheck (3 min ctx, 2 min script), tryPlugin (2 min), authorizeSSHKey (1 min ssh) and saveHost/setHostSSH (which run a login test) all routinely run past 30s. The handler keeps going and the action completes, but the buffered response write fails and the browser gets an empty reply or connection reset. The agent's own server already handles this with http.NewResponseController(w).SetWriteDeadline (internal/agent/update.go:136), and the api package does not. maxTestTimeout in checks.go shows the limit is known about but only applied to the check test.
- **When it bites:** The admin clicks 'Reinstall agent' on a slow server, or restarts a unit whose stop takes 45s (systemd's default TimeoutStopSec is 90s). The work finishes, but the dashboard shows a network error with no output. The admin retries and reinstalls or restarts again, or wrongly concludes the change failed. A media cleanup scan over a large library does the same: deletions happen, but the UI reports failure.
- **Fix:** In the long-running handlers, call http.NewResponseController(w).SetWriteDeadline(time.Now().Add(budget+slack)), or run them as background jobs the client polls, as apps and updates already do. Alternatively, raise WriteTimeout above the largest handler budget.

#### 13. clientIP trusts a client-supplied X-Real-IP through a local reverse proxy, so the login throttle can be bypassed and the owner can be locked out on purpose

`api-auth-1` (also reported as x-security-2) · security · verifier: **confirmed** · [backend/internal/api/server.go:347](backend/internal/api/server.go#L347)

- **Status (2026-11-09):** ✅ fixed. Api/server.go clientIPs validates and takes the right-most hop; learnProxy.
- **What:** When RemoteAddr is loopback, which is the case behind nginx, Caddy or cloudflared on the same box, clientIP returns X-Real-IP exactly as received. Most proxy setups only set X-Forwarded-For (for example nginx `$proxy_add_x_forwarded_for`) and pass the client's own X-Real-IP header through unchanged. That clientIP value is the key for every throttle (readCredentials, auth, createAdmin, CheckCode failures). It is also what gets written to the activity log ("Signed in from …") and what the firewall's keep-reachable guard uses.
- **When it bites:** The dashboard sits behind nginx with only X-Forwarded-For configured. (a) An attacker sends POST /api/auth/login with a new random `X-Real-IP: 10.x.y.z` on every request. throttled() never reaches 10 for any key, so password, 2FA-code and setup-code guessing is never limited. recordFailure also fills the map past 1024 entries and evicts real entries at random. (b) The attacker sends 10 bad tokens with `X-Real-IP: <owner's IP>`. auth() checks throttled(ip) before the credentials, so the owner's valid session gets 429 on every API call. Repeating this each minute locks the owner out indefinitely. (c) The activity log records a forged source IP for sign-ins.
- **Fix:** Do not trust X-Real-IP unless an explicit TRUSTED_PROXY setting is enabled, and prefer the right-most untrusted X-Forwarded-For hop. Validate the result with net.ParseIP and fall back to RemoteAddr when it does not parse.

#### 14. WEB_AUTH_TOKEN can be brute-forced with no throttle through public /api/config and /health

`gap-1` · security · verifier: **found by completeness critic (single reviewer, not independently re-verified)** · [backend/internal/api/server.go:457](backend/internal/api/server.go#L457)

- **Status (2026-11-09):** ✅ fixed. Api/server.go access() throttles wrong tokens; 16-character minimum token.
- **What:** auth() checks throttled(ip) and calls recordFailure for a wrong bearer token. But the public handlers config() (server.go:457), health() (server.go:552) and publicAppearance() (appearance.go:79) call s.authorized(r) directly and reveal the result: /api/config returns "authorized": true/false, and /health returns the detailed body only to a valid token. None of them checks throttled() or records a failure. WEB_AUTH_TOKEN is set by the owner in .env with no length or strength check (settings.go:412, env.recommended:281). It is compared with a cheap constant-time compare (no PBKDF2), and it grants full API access, which as root includes updates, unit control, firewall and plugins.
- **When it bites:** The owner sets WEB_AUTH_TOKEN=mediabox2024 and exposes the dashboard. An attacker sends GET /api/config with 'Authorization: Bearer <guess>' thousands of times a second. The 10-failure IP lockout never triggers because these requests never reach auth(). A response with "authorized":true confirms the token, which then works on every write endpoint.
- **Fix:** Make public endpoints check credentials through the same path as auth(). If a token is presented and wrong, call recordFailure, and refuse to evaluate the token while the IP is throttled. Alternatively, report 'authorized' only from the session cookie on public endpoints. Consider enforcing a minimum token length at startup.

#### 15. collectStats clears Cmdline, RSS and VMS on process slices shared with the scanner's cache (data race, and alerts lose data)

`api-auth-6` (also reported as x-contract-4) · concurrency · verifier: **confirmed** · [backend/internal/api/server.go:627](backend/internal/api/server.go#L627)

- **Status (2026-11-09):** ✅ fixed. Api/server.go clones TopCPU/TopMemory before clearing fields.
- **What:** ProcessScanner.Overview returns `o := *s.cached` with TopCPU and TopMemory reslices that share their backing array with the cached overview (and with whatever the monitor loop already holds). collectStats then writes `list[i].Cmdline, list[i].RSS, list[i].VMS = nil, 0, 0` into those shared elements, outside the scanner's mutex. The loop calls Overview(5), the same limit, for process and temperature alerts, and reuses the cached scan for up to 2s.
- **When it bites:** The dashboard polls /api/stats every 2s, so within the 2s cache window the loop's processAlerts and temperature alert receive processes with Cmdline=nil and RSS=0. ProcessAlert drops the memory size (it is only shown `if p.RSS > 0`), and processReasons no longer recognises download or media processes by command line, so the 'Likely why' analysis is wrong. The loop may also be reading these elements while the API goroutine writes them, a data race the race detector would flag under load.
- **Fix:** Copy before modifying: `cp := slices.Clone(list)`, then clear fields on cp and assign it back to st.Processes. Better still, have Overview return deep-copied slices.

#### 16. SetupNginxLogs hands the nginx log dir to www-data, then creates and chowns files through symlinks as root (CVE-2016-1247 pattern)

`host-collect-4` · security · verifier: **confirmed** · [backend/internal/cleanup/disk.go:449](backend/internal/cleanup/disk.go#L449)

- **Status (2026-11-09):** ✅ fixed. Cleanup/disk.go SetupNginxLogs uses os.OpenRoot, O_EXCL|O_NOFOLLOW, fchown.
- **What:** When MANAGE_NGINX_LOGS is on, SetupNginxLogs runs `chown www-data:adm /var/log/nginx`, which makes the directory writable by the web user. It then does os.Stat(p) (a dangling symlink reports NotExist), os.Create(p) (follows the symlink and creates the target as root) and `chown www-data:adm p` (follows symlinks, no -h). It runs after every cleanLogs pass that cleaned anything.
- **When it bites:** An attacker with code execution as www-data (for example a PHP RCE) deletes /var/log/nginx/error.log and replaces it with a symlink to /etc/ld.so.preload. On the next disk-pressure log cleanup, nightjar (root) creates /etc/ld.so.preload and chowns it to www-data. The attacker writes a path to a malicious .so into it, and the next root process loads it: local privilege escalation to root.
- **Fix:** Never chown the directory to the service user (keep it root:adm 0755). Create files with os.OpenFile(p, O_CREAT|O_EXCL|O_NOFOLLOW, 0640) through an os.Root on the log dir, and use f.Chown (fchown) on the opened descriptor instead of the chown command. Also validate NGINX_SERVICE_NAME with serviceNameRE.

#### 17. DirSize/LargestSubdirs cross filesystem boundaries and can block the monitoring loop indefinitely on network mounts

`host-collect-11` · resource-leak · verifier: **confirmed** · [backend/internal/cleanup/disk.go:469](backend/internal/cleanup/disk.go#L469)

- **Status (2026-11-09):** ✅ fixed. Cleanup/disk.go LargestSubdirs skips other devices; 10s budget.
- **What:** DirSize walks with no device check, so LargestSubdirs("/") descends into /mnt, /media, /home on other disks, NFS/CIFS/sshfs mounts and so on (only proc, sys, dev and run are skipped). The deadline is checked only between callbacks, so a single stat on a hung hard-mounted NFS share blocks forever. It runs synchronously inside diskAlerts.alertPartitions and alertTotal in Evaluate, with 15s per subdirectory and no overall cap, and once per partition.
- **When it bites:** Root reaches the alert threshold on a box with a NAS mounted at /mnt/nas. The alert lists '/mnt 12 TB' as the largest thing 'inside' /, which is wrong. If the NAS is unreachable, the stat hangs in D state and the whole monitor loop stops: no more checks, heartbeats or alerts.
- **Fix:** Record the root's st_dev and SkipDir any directory on a different device, as CleanTree's deviceOf does. Bound the total time across all subdirectories, and run the sizing off the main loop with a context.

#### 18. Security and audit logs are protected from truncation but their rotated archives (auth.log.N, audit.log.N, wtmp.1, btmp.1) are deleted

`host-collect-10` · data-safety · verifier: **confirmed** · [backend/internal/cleanup/logs.go:165](backend/internal/cleanup/logs.go#L165)

- **Status (2026-11-09):** ✅ fixed. Cleanup/logs.go RotatedLogs skips audit logs; Samba log.<ip> excluded.
- **What:** isProtectedLog exists so that auth.log, secure, audit.log, btmp, wtmp and anything under an 'audit' directory are 'only ever trimmed'. RotatedLogs and CleanupRotated never consult it, and the walk descends into /var/log/audit. Routine passes delete every rotated archive older than ROTATED_LOG_RETENTION_DAYS (default 7) or larger than the threshold, and aggressive passes delete all of them. The `\.\d+$` pattern also matches live files such as Samba's per-client `log.192.168.1.10`.
- **When it bites:** auditd keeps audit.log.1..4 and logrotate keeps 4 weeks of auth.log.N.gz and btmp.1. Nightjar silently deletes every audit and auth archive older than 7 days on each routine pass, and all of them as soon as any disk crosses the aggressive threshold, destroying the audit trail needed after an intrusion.
- **Fix:** Apply isProtectedLog to rotated files as well, by stripping the rotation suffix and checking the base name and the audit path component, and skip the audit directory in RotatedLogs.

#### 19. Host bandwidth sums every interface including loopback, bridges and veth, inflating rates and causing false network alerts

`host-collect-9` · correctness · verifier: **confirmed** · [backend/internal/collect/host.go:231](backend/internal/collect/host.go#L231)

- **Status (2026-11-09):** ✅ fixed. Collect/host.go NetworkIO sums physical NICs only.
- **What:** NetworkIO uses gopsutil net.IOCounters(false), which sums every line of /proc/net/dev, including lo, docker0, br-* and veth*. BandwidthMeter rates, NetworkMBps (used by networkAlert against NETWORK_BANDWIDTH_THRESHOLD), the analytics Rx/Tx history and the temperature alert's net fields are all built on this total. Container traffic crosses veth, the bridge and the physical NIC, so it is counted 2-3 times, and local traffic (reverse proxy to app, Plex to localhost) is counted too even though it never leaves the host. The netload package already handles this correctly with Virtual and Default flags.
- **When it bites:** Plex in Docker streams 10 MB/s to a client: veth, docker0 and eth0 each count it, so the dashboard shows about 30 MB/s and a 20 MB/s network alert fires although the uplink carries 10 MB/s. A backup over 127.0.0.1 triggers the alert with no external traffic at all.
- **Fix:** Sum only physical interfaces (those with /sys/class/net/<if>/device), or the default-route interface, and skip lo and virtual devices. The netload describe()/defaultInterface helpers can be reused.

#### 20. LOG_FORMAT=json is always rejected, so JSON logging can never be turned on

`storage-config-main-3` · correctness · verifier: **confirmed** · [backend/internal/config/fields.go:175](backend/internal/config/fields.go#L175)

- **Status (2026-11-09):** ✅ fixed. Config/fields.go choice matches case-insensitively; LOG_FORMAT=json works.
- **What:** choice() upper-cases the raw value before testing it against `options`. That works for LOG_LEVEL and ALERT_CHANNELS_MIN_SEVERITY, whose options are upper case. LOG_FORMAT's options are lower case: {"text","json"} (settings.go:418). "json" becomes "JSON", which is not in the list. The default ("text") is only accepted because the `raw == ""` branch assigns def after the ToUpper. An overlay test confirms that LoadFrom({LOG_FORMAT: json}) logs `Invalid setting, using default ... err="must be one of text, json"` and returns "text".
- **When it bites:** An operator sets LOG_FORMAT=json in .env, as README.md:454 and frontend/src/docs/logs.md:23 describe, to feed Loki, Vector or Datadog. Nightjar warns and falls back to text, and main.go:90 passes JSON:false. Log shippers expecting JSON lines get aligned text lines instead and fail to parse them.
- **Fix:** Compare case-insensitively and keep the canonical option spelling, e.g. find i with strings.EqualFold(options[i], raw) and assign options[i]. Or stop upper-casing and normalise per field.

#### 21. Startup cleanup can write a literal "?" into the nginx include file, breaking nginx's config

`ddos-firewall-3` · error-handling · verifier: **confirmed** · [backend/internal/ddos/defend.go:389](backend/internal/ddos/defend.go#L389)

- **Status (2026-11-09):** ✅ fixed. Ddos/defend.go webStale flag replaces the "?" sentinel.
- **What:** startLocked() sets d.web = "?" as a sentinel and calls writeWebLocked(ctx, "") when a stale block file exists. writeWebLocked removes the file and runs `nginx -t`. If the test fails, it 'restores' the previous content. Because previous == "?" and not "", it takes the else branch and runs os.WriteFile(d.WebBlockFile, []byte("?")). That writes `?` into /etc/nginx/conf.d/nightjar-blocked.conf, which is invalid nginx syntax. d.web stays "?", so every later sample repeats remove → nginx -t → write "?". The same happens on the !d.on() path.
- **When it bites:** Nightjar starts while a stale nightjar-blocked.conf exists and some unrelated part of the nginx config is temporarily broken (for example a missing certificate after a failed renewal). Nightjar keeps a `?` file in conf.d almost all the time. The admin fixes the real problem and restarts nginx, which then fails with "unexpected end of file" in nightjar-blocked.conf. If Nightjar stops or crashes in this state, nginx fails to start at the next boot and all sites go down.
- **Fix:** Do not use content as a sentinel. Track 'unknown' with a separate bool, or set previous to "" for the sentinel case, so a failed test after removing never writes the sentinel back to disk.

#### 22. DDoS defender can be made to block arbitrary third-party addresses with spoofed SYNs, and the block drops their replies too

`x-security-3` · security · verifier: **plausible** · [backend/internal/ddos/defend.go:431](backend/internal/ddos/defend.go#L431)

- **Status (2026-11-09):** ✅ fixed. Ddos/defend.go blocks only on established connections or request counts.
- **What:** blockLocked blocks any source with at least 100 SYN_RECV entries in /proc/net/tcp. Half-open entries come from a single unanswered SYN, so the source address is trivially spoofed. The block is written as `-A NIGHTJAR -s <addr> -j DROP` (line 656), and that chain is jumped to first from INPUT for all traffic with no conntrack NEW match. Replies to the host's own outbound connections from that address are dropped too. The trust list holds only private ranges, Cloudflare, configured remote hosts and sources with a live SSH session. It leaves out the system DNS resolvers, the admin's public IP when no SSH session is open, and upstream APIs. Defense is on by default (Meta(DefendKey) != "off").
- **When it bites:** An attacker sends about 10 spoofed SYN/s to port 443, about 2/s of them with source 8.8.8.8 (the host's resolver) and the rest from random sources. That is enough to pass the 512 SYN_RECV detection threshold, and 8.8.8.8 alone accumulates more than 100 half-open entries within the ~60 s SYN-ACK retry window. The defender adds `-s 8.8.8.8 -j DROP`, DNS replies are dropped, and Plex/arr/Discord/Telegram alerts and update checks fail for 15-minute blocks, renewed for as long as the attacker continues. Spoofing the admin's home IP locks them out of the dashboard the same way. On agents, spoofing the dashboard's IP after an agent restart (its dashboards map is empty then) cuts off monitoring.
- **Fix:** Do not block on SYN_RECV counts alone: SYN floods are already handled by the rate-limit and syncookie measures. Block only addresses with ESTABLISHED connections or logged web requests. Restrict DROP rules to `-m conntrack --ctstate NEW`. Add /etc/resolv.conf nameservers and the default gateway to the trust list.

#### 23. Any unauthenticated TCP connection to sshd counts as an SSH login: the attacker is trusted by the DDoS defender, and the dashboard refuses rules that would block it

`ddos-firewall-1` · security · verifier: **confirmed** · [backend/internal/ddos/read.go:141](backend/internal/ddos/read.go#L141)

- **Status (2026-11-09):** ✅ fixed. Ddos/ssh.go counts only real sshd sessions as logins.
- **What:** ParseTCP sets Peer.SSH = true for any ESTABLISHED socket whose local port is an sshd port. That is only a completed TCP handshake. No authentication is involved. Two places treat this flag as 'logged in over SSH'. In ddos/defend.go:423 and :728-731, any such source is never blocked. It is also added to the trust list, so it gets an `-s <ip> -j RETURN` at the top of the NIGHTJAR chain and skips every limit (SYN hashlimit, connlimit, UDP limits, stray-TCP drops). In firewall/firewall.go:556 and :836-838, sshLogins() feeds the same flag into guard(), so a block rule that covers the SSH port is refused for that address.
- **When it bites:** (a) During an HTTP or connection flood, each bot also opens one TCP connection to port 699/22. sshd keeps pre-auth connections for LoginGraceTime (120s by default). The bots are then exempt from every limit and block for the whole attack. (b) An admin tries to block an SSH brute-forcer, for example `block 203.0.113.7` on all ports or on port 22, while the bot has a connection open, which is most of the time. Add() fails with "that would cut off an SSH login (203.0.113.7, port 22)", so the most common firewall task cannot be done.
- **Fix:** Do not infer 'logged in' from TCP state. Use real login sessions instead: utmp/wtmp (`who`), sshd child processes with an authenticated user (`sshd: user@pts`), or the peer address of the SSH_CONNECTION env of logged-in sessions. Use that set both for the defender's trust list and for firewall guard().

#### 24. SSH ports are read once from the main sshd_config only, so default-drop can lock SSH out and SSH sessions go unrecognised

`ddos-firewall-5` · correctness · verifier: **confirmed** · [backend/internal/ddos/read.go:186](backend/internal/ddos/read.go#L186)

- **Status (2026-11-09):** ✅ fixed. Ddos/ssh.go SSHConfigPorts follows Include and ListenAddress.
- **What:** sshPorts is a package-level var computed once at process start. It only reads /etc/ssh/sshd_config and only accepts lines with exactly two fields (`Port N`). It ignores `Include /etc/ssh/sshd_config.d/*.conf` (the Ubuntu/cloud-init default, present on this host), `ListenAddress host:port`, `Port 2222 # comment`, and port changes made after Nightjar started. These ports are what firewall.openLocked() keeps open under default "drop" (firewall.go:333, build.go:67-73). They are also what marks SSH sessions for guard() and the defender's trust list.
- **When it bites:** The SSH port is set in /etc/ssh/sshd_config.d/10-port.conf as `Port 2222`, or set in sshd_config after Nightjar started. Nightjar believes SSH is on 22. The admin sets the default to "drop" and clicks Keep, because the dashboard still works. All new SSH connections to 2222 are now dropped and only the existing session survives: an SSH lockout. Admin sessions on 2222 are also not recognised, so guard() will not protect them from a block rule, and the defender does not trust them.
- **Fix:** Get the ports from `sshd -T` (effective config, handles Include/Match/ListenAddress), or from sshd's listening sockets in /proc/net/tcp* matched to the sshd process. Re-read them on every apply instead of once at init.

#### 25. In big HTTP floods, per-source request counts are compared to an extrapolated total, so flooding sources escape blocking

`ddos-firewall-4` · correctness · verifier: **confirmed** · [backend/internal/ddos/read.go:381](backend/internal/ddos/read.go#L381)

- **Status (2026-11-09):** ✅ fixed. Ddos/read.go scales each source's count like the total.
- **What:** When more than 8 MiB of log arrives in one interval, LogTail.Requests parses only the first 8 MiB into bySource[...].Requests. It then scales the returned total up to the whole size and skips to the end. blockLocked compares the unscaled per-source count with the scaled total (`src.Requests*20 >= s.Requests`). The rate check also uses the unscaled count. Each source's share is therefore understated by size/8MiB, exactly in the largest floods.
- **When it bites:** An HTTP flood writes about 40 MiB of access log per 5s sample (~200k lines). Ten bots each send 10% of it. Of the parsed ~40k lines, each bot has ~4000, and 4000*20 = 80,000 < 200,000 (the extrapolated total). No bot meets the 5% rule and nothing is blocked, although each bot really sends 10% of the traffic.
- **Fix:** Scale the per-source counts by the same factor as the total, or compare against the parsed count (count before extrapolation) when computing shares and rates.

#### 26. Incident split by the 15-minute Join gap is never closed (phantom open incidents)

`notify-incidents-1` · correctness · verifier: **confirmed** · [backend/internal/incidents/incidents.go:155](backend/internal/incidents/incidents.go#L155)

- **Status (2026-11-09):** ✅ fixed. Incidents.go maps subject to its incident across the whole scan.
- **What:** Build tracks unrecovered subjects in a per-incident `open` map. When a new problem arrives more than Join (15 min) after `last`, finish() is called: an incident with open subjects is appended with Ended=nil and `open` is reset to an empty map (line 155). The later recovery for that subject is then checked against the *new* incident's `open` map (line 175), fails, and is attached as a plain note. The first incident therefore stays open forever. With the default gate (ALERT_COOLDOWN 300, backoff 2) a check that stays down re-sends "X is down" at t=0, 300, 900, 2100: the 1200s gap after 900 exceeds Join, so any outage longer than ~35 minutes produces this.
- **When it bites:** check:plex goes down at t=0 and stays down; re-alerts at 300s and 900s join incident A; the re-alert at 2100s starts incident B (A finished with Ended=nil, open reset); Plex recovers at 3000s -> recovery closes B only. A is reported as ongoing in /api/incidents indefinitely, the weekly report counts it in Open and adds (now - start) to Down, and a published status-page notice for A shows 'unavailable ... We are working on it' forever (publicNotices copies inc.Ended=nil).
- **Fix:** Keep a single map subject->incident across the whole scan (do not reset it in finish), and route an ok event to whichever incident still holds that subject open; alternatively do not split an incident while it still has open subjects (or measure the Join gap from a re-alert of an already-open subject as continuation).

#### 27. EDAC counter keys collapse to 'edac_<file>' for every controller and csrow, causing false daily memory-error alerts

`host-collect-6` · correctness · verifier: **confirmed** · [backend/internal/kernel/monitor.go:463](backend/internal/kernel/monitor.go#L463)

- **Status (2026-11-09):** ✅ fixed. Kernel/monitor.go EdacCounts reads only mcN ce/ue counts; old keys migrated.
- **What:** memoryController returns the first path component starting with 'mc' or 'edac'. For /sys/devices/system/edac/mc/mc0/csrow0/ce_count that is always 'edac', so mc0/ce_count, mc1/ce_count, mc0/csrow*/ce_count and *_ce_noinfo_count all share one LastEdacCounts key. The walk also visits every file twice (roots .../edac/mc and .../edac). Each visit compares against whatever same-named file was visited last.
- **When it bites:** A server with ECC has static historical counts, for example mc0 total 120 and csrow0/csrow1 at 100/20. In each pass the key sees 120 -> 100 -> 20 -> 120, a +100 'increase' that is >= 10% of 20, so the crash webhook gets 'Correctable memory error detected: 100 new errors' every 24h with no new errors. Any nonzero UE count gives a daily 'Uncorrectable' alert. Real new errors on one controller are masked by the shared cooldown, and Source shows 'EDAC-edac'.
- **Fix:** Key by the path relative to /sys/devices/system/edac/mc (for example 'mc0/csrow0/ce_count'), walk only .../edac/mc, and use the top-level mcN counters for alerting.

#### 28. kernelLogCrashes has no time filter: old crashes re-alert after every restart or seen-cache reset, and one Oops is counted up to 5 times

`host-collect-8` · correctness · verifier: **confirmed** · [backend/internal/kernel/monitor.go:683](backend/internal/kernel/monitor.go#L683)

- **Status (2026-11-09):** ✅ fixed. Kernel/monitor.go skips kern.log lines older than the cutoff; dedupes across sources.
- **What:** kernelLogCrashes checks only the file's mtime against the cutoff (syslog is always fresh). It then reports every kernel crash line in the last 1000 lines of kern.log, messages and syslog, deduplicated only by the in-memory `detected` map. That map is empty after a nightjar restart and is wiped when it reaches 10000 entries. The same oops is also reported from the journal (once per matching keyword: 'BUG:' and 'Unable to handle' are 2 events), dmesg, kern.log and syslog.
- **When it bites:** A kernel BUG happened 3 days ago and is still in kern.log's tail. After every nightjar restart or update (or once `detected` hits 10000 and resets), a Critical 'The kernel crashed' alert fires again. A single fresh oops produces 'The kernel crashed 5 times'.
- **Fix:** Parse each syslog line's timestamp and skip lines older than the cutoff, or persist a high-water mark (file inode plus offset). Deduplicate across sources by normalised message text and count crashes, not keyword matches.

#### 29. Keep clicked during a cleanup pass does not stop the removal, and the deleted item then shows as Kept

`integrations-2` · concurrency · verifier: **confirmed** · [backend/internal/mediacleanup/mediacleanup.go:391](backend/internal/mediacleanup/mediacleanup.go#L391)

- **Status (2026-11-09):** ✅ fixed. Mediacleanup.go re-reads Kept under the lock before each removal.
- **What:** scan() loads the state once under m.mu (line 270). It then does slow work without the lock: arr List, per-item arr Downloads, qBittorrent List and Plex calls. The removal loop (380-403) runs on that stale snapshot and never re-checks st.Kept before calling m.remove. Only after the deletions does it reload latest.Kept (408-412), and then it drops the item record (including its Removed status) because the key is now kept.
- **When it bites:** An item is due today. The 6-hourly pass (or a manual Scan) starts, and while it is listing Sonarr/Radarr history the owner clicks Keep. Keep() saves Kept[key] and returns 200. The pass then deletes the torrent, the files and the Sonarr series anyway. The final merge deletes st.Items[key], so the dashboard shows the title under Kept with no trace that it was removed.
- **Fix:** Right before each m.remove, take m.mu, reload the stored Kept set, and skip the item if it is now kept. Keep() could also refuse, or warn, while m.running is held for items that are due.

#### 30. Long synchronous disk work with no heartbeat makes the 30s watchdog fire false 'Nightjar stopped checking' alerts and can restart Nightjar mid-cleanup

`monitor-core-4` · correctness · verifier: **confirmed** · [backend/internal/monitor/disk.go:190](backend/internal/monitor/disk.go#L190)

- **Status (2026-11-09):** ✅ fixed. Monitor/context.go withTimeout heartbeats; largest has a 10s budget.
- **What:** The watchdog (default timeout 30s, always started) fires when no heartbeat arrives within Timeout. Several loop-goroutine operations run far longer than that without a heartbeat: LargestSubdirs (15s per top-level subdirectory, run one after another, and WalkDir crosses into mounted media under /mnt) in alertPartitions (line 190) and alertTotal (line 216); mediaCheck DirSize, up to 60s (resources.go:325); diskCleanup.emergency, up to 10 min, with a heartbeat only after it (line 119); partition cleanups, up to 5 min; cleanLogs, up to 5 min. The FreezeMonitor has the same flaw: its heartbeat is set at cycle start and checked later in the same cycle, so any slow cycle reads as a 'System Freeze'. mediaCheck also ignores DirSize's `partial` result, so a tree that cannot be walked in 60s is compared against the threshold using an undercount.
- **When it bites:** Root crosses DISK_THRESHOLD. alertPartitions sizes /usr, /var, /home and /mnt one by one at up to 15s each, the loop stalls for minutes, and the watchdog sends a Critical 'Nightjar stopped checking' alert, exactly when the disk is filling. With WATCHDOG_AUTO_RESTART on, it runs `systemctl restart nightjar` mid-cleanup. On restart lastCleanup and the gates reset, the same slow path runs again, and the service restarts every WATCHDOG_RESTART_COOLDOWN.
- **Fix:** Call ctx.Heartbeat() inside long walks: pass a progress callback to DirSize/LargestSubdirs, or heartbeat between subdirectories. Better, move the 'largest directories' sizing and the cleanups off the loop goroutine with a single-flight guard. Give LargestSubdirs a total budget, and keep it on the same device (compare st_dev). Treat a partial DirSize as 'at least' and log it.

#### 31. qBittorrent tracking cap evicts live torrents every check when more than 1000 exist, resetting stall and ratio state

`monitor-core-5` · correctness · verifier: **confirmed** · [backend/internal/monitor/torrents.go:93](backend/internal/monitor/torrents.go#L93)

- **Status (2026-11-09):** ✅ fixed. Monitor/torrents.go deletes only torrents no longer live.
- **What:** Torrents no longer listed are already removed from h.tracked (lines 88-92), so the map's size equals the number of live torrents. When that is over maxTrackedTorrents, the cap evicts the entries with the oldest seenAt. seenAt is set only on creation (line 136) and never refreshed. Evicted torrents are still live, so they are re-created next check with fresh state and become the newest, and a different batch is evicted. Every torrent's state is wiped every few checks.
- **When it bites:** A seedbox with 1500 torrents loses 500 tracked entries every check (~10s), so each torrent is reset roughly every 30s. stalledSince restarts each time, so a stall never reaches STALLED_TORRENT_THRESHOLD and auto-recovery/replacement never happen. lastRatioAction goes back to 0, so a ratio or inactive removal that keeps failing is retried, and a Discord ratio/inactive alert is sent, on almost every check instead of after SEEDING_RATIO_ACTION_COOLDOWN.
- **Fix:** Drop the cap, since the map is already bounded by the live list, or evict only entries that are not live. If a cap is kept, refresh tr.seenAt = now in process() on every sighting.

#### 32. Webhook URLs and bot tokens leak through delivery errors into notification history, test results and logs

`notify-incidents-3` (also reported as x-security-4, storage-config-main-4) · security · verifier: **confirmed** · [backend/internal/notify/hub.go:361](backend/internal/notify/hub.go#L361)

- **Status (2026-11-09):** ✅ fixed. WithoutURL strips URLs from errors; Redact covers Telegram and Slack.
- **What:** Network failures from http.Client are *url.Error, whose Error() embeds the full request URL. For Discord webhooks (client.go:290) that is https://discord.com/api/webhooks/<id>/<token>?with_components=true; for Telegram (channels.go:75) it is https://api.telegram.org/bot<TOKEN>/sendMessage; for Slack/generic webhook the secret URL itself. These strings become LastError(), are stored unredacted in notifications.error by deliver (hub.go:361), returned by GET /api/notifications and by notify.Test in POST /api/alerts/test and /api/settings/test, while the settings API deliberately never returns these secret fields. Additionally notify.go:65 logs the error; logx.Redact only masks discord.com/discordapp.com webhook paths and key=value secrets, so Telegram bot tokens and Slack webhook URLs are written in clear to the 0640 log file and the dashboard log viewer.
- **When it bites:** DNS is briefly unavailable at boot; Telegram send fails with `Post "https://api.telegram.org/bot123456:AAH.../sendMessage": dial tcp: lookup api.telegram.org: ...`. The bot token is now in the DB notification row, in the dashboard's notifications list, in /var/log nightjar log (group-readable) and the in-memory log viewer, bypassing the secret-masking the settings page enforces.
- **Fix:** Strip the URL from errors before recording them (unwrap *url.Error and use only .Err, or run logx.Redact extended with patterns for api.telegram.org/bot<token>, hooks.slack.com/services/..., and the configured ALERT_WEBHOOK_URL), and apply it in delivery.record, Webhook.setError and Bot error paths.

#### 33. Hub's failed-delivery map collapses later alerts with the same title into an old record, dropping them from history

`notify-incidents-2` · data-safety · verifier: **confirmed** · [backend/internal/notify/hub.go:369](backend/internal/notify/hub.go#L369)

- **Status (2026-11-09):** ✅ fixed. Notify/hub.go keys retries on area+subject+title+body with a 1h window.
- **What:** deliver() keys `h.failed` by area+title only and never expires entries. If any earlier alert with that area+title failed on every channel, the next alert with the same area+title (any time later in the process lifetime, different subject/body/timestamp) does not insert a new notification; it just calls SetNotificationStatus on the old row (status/channels/error only). The new occurrence's ts, body, severity and subject are never stored. Callers do not actually retry (raiseAlert marks the gate sent regardless), so this mostly hits unrelated later alerts. Entries for titles that never repeat (e.g. 'CPU at 93%') also accumulate forever.
- **When it bites:** Internet drops: 'Plex is down' (area uptime) fails on Discord -> failed[uptime\x00Plex is down]=id 100. Later 'Plex is back up' is delivered. Three days later Plex goes down again; 'Plex is down' is delivered and only row 100 (dated three days ago) is flipped to 'sent'; no new row. The notification history lacks the outage, incidents.Build never sees the new problem (its recovery is ignored because the subject is not open), and the weekly report/uptime incident list miss it. If row 100 was already pruned, the alert vanishes entirely.
- **Fix:** Always insert a new record for a new alert; only reuse a failed record for a genuine retry of the same alert (e.g. key on area+subject+title+timestamp, or have the caller pass the record id), and expire failed entries after a short window.

#### 34. EverWatched silently stops at 100,000 history entries, so cleanup treats items watched beyond that as never watched

`integrations-5` · data-safety · verifier: **confirmed** · [backend/internal/plex/client.go:614](backend/internal/plex/client.go#L614)

- **Status (2026-11-09):** ✅ fixed. Plex/client.go errors at the cap or on short reads.
- **What:** EverWatched pages through /status/sessions/history/all with `for start := 0; start < 100000; start += page`. When the cap is reached it returns the partial map with a nil error. mediacleanup.scan relies on this map as its proof that nobody watched an item (its comment says that without the whole history it stops), but here truncation is not reported as an error. Offset paging over a live list can also skip entries when new plays arrive mid-scan.
- **When it bites:** On a long-running multi-user server with a music library, every track play is a history row, so 100k rows is reachable. Movies or shows watched only by other accounts and recorded past the cap are missing from `watched`. The owner's own viewCount says nothing about those accounts, so the item qualifies, is announced, and is deleted with its files a week later.
- **Fix:** Use MediaContainer.totalSize and return an error when fewer entries were read than totalSize, or when the cap is hit, so the scan aborts. Or page by viewedAt cursor instead of a fixed cap.

#### 35. A broken plugin is listed with "provides": null and crashes the Plugins page and the check form's plugin picker

`x-contract-6` · api-contract · verifier: **confirmed** · [backend/internal/plugins/plugins.go:201](backend/internal/plugins/plugins.go#L201)

- **Status (2026-11-09):** ✅ fixed. Plugins.go Provides defaults to an empty list.
- **What:** load() returns early for a folder with no plugin.json (line 201) or invalid JSON (line 206), and problem() accepts a manifest without "provides" (line 223), so Manifest.Provides (`json:"provides"`, no omitempty) stays nil. These plugins are listed on purpose so their problem can be shown, but they encode "provides": null. The frontend type is PluginProvides[] (non-null), and the UI calls p.provides.filter(...) (pages/Plugins.tsx:151), p.provides.includes (Plugins.tsx:118, 200-252) and r.plugins.filter((p) => p.provides.includes('check')) (pages/CheckForm.tsx:120).
- **When it bites:** The owner drops a folder into the plugins directory without a plugin.json yet, or with a typo in it, and presses Rescan. The Plugins page throws a TypeError and shows nothing, including the problem text meant to explain what is wrong. On the check form, choosing type 'plugin' never loads the plugin list, because the .then callback throws and the plugins state stays undefined.
- **Fix:** Initialise p.Provides = []string{} in load() (or in List()), and use (p.provides ?? []) in the UI.

#### 36. Renaming a check while it is running leaves it marked running forever, so it never runs again

`integrations-3` · correctness · verifier: **confirmed** · [backend/internal/services/manage.go:72](backend/internal/services/manage.go#L72)

- **Status (2026-11-09):** ✅ fixed. Services/manage.go keeps running/stale on reset; routes results across renames.
- **What:** Claim sets st.running=true on the State object. Save() with a new name moves that same *State under spec.Name and deletes oldName (72-76), but leaves running untouched. The goroutine that is still checking later calls Record(oldName, ...) (monitor/services.go:49 uses the claimed spec.Name). Record doesn't find oldName, returns without clearing running, and drops the result. Claim skips states with running==true, so the renamed check is never claimed again until the process restarts. RunSoon cannot unstick it. A related case: a type or target change resets the State (running=false) while the old run is in flight, so the old target's result is recorded against the new spec and two runs can overlap.
- **When it bites:** An HTTP check to a down host spends its full 10s timeout on each attempt, retrying every 20s, so it is running about half the time. The user opens it and renames "api" to "api-prod" during an attempt. From then on the check stays frozen on its last status and LastCheck, never alerts again, and the public status page keeps showing the stale state.
- **Fix:** Give each claim a token or generation. Have Record find the state by that token rather than by name, or carry st.running over and clear it when a stale report comes in. On a type/target reset, bump the generation so stale results are discarded.

#### 37. smartctl 'device open failed' is classified as 'asleep', so a dead or vanished drive keeps its last good reading and never alerts

`host-collect-5` · correctness · verifier: **confirmed** · [backend/internal/smart/smart.go:303](backend/internal/smart/smart.go#L303)

- **Status (2026-11-09):** ✅ fixed. Smart.go asleep only on a standby message; open failures set Error.
- **What:** asleep() falls back to `ModelName == "" && ExitStatus&2 != 0 && SmartStatus == nil`. smartctl's bit 1 (value 2) means 'device open failed, did not return IDENTIFY, or is in low-power mode', so any open or IDENTIFY failure counts as standby. Real standby is already caught by the message check above it. readOne then returns the previous reading with Asleep=true and Error cleared. driveWatcher only compares drives with Checked == now (monitor/system.go), so neither this case nor the error case ever produces an alert.
- **When it bites:** A disk stops responding: still listed by --scan, but open or IDENTIFY fails, and smartctl exits with 2 and the message 'Smartctl open device: /dev/sdb [SAT] failed: ...'. Nightjar shows the drive as 'asleep' with health 'good' from its last reading and sends no alert, so a failing disk is hidden.
- **Fix:** Treat a drive as asleep only when a message mentions STANDBY/SLEEP. Report other exit-bit-2 failures as an Error, and have driveWatcher alert when a previously healthy drive becomes unreadable.

#### 38. Service installed via sudo runs as SUDO_USER with NoNewPrivileges=true, so every root operation (sudo -n fallback) fails

`updates-apps-5` · correctness · verifier: **confirmed** · [backend/internal/systemd/service.go:89](backend/internal/systemd/service.go#L89)

- **Status (2026-11-09):** ✅ fixed. Systemd/service.go always writes User root.
- **What:** Install requires euid 0, and the documented path is `make install-service`, which runs `sudo ./bin/nightjar -install-systemd`. Install then sets User= to SUDO_USER, so the service runs as the invoking non-root user. The same unit sets NoNewPrivileges=true and RestrictSUIDSGID=true, so sudo cannot gain privileges. updates.Exec relies on `sudo -n` for Root commands when not root (apt-get update/upgrade, snap refresh, systemctl restart, release update commands) and on `sudo -n -u` for git as the checkout owner. All of these fail with 'no new privileges flag is set'. Firewall and app-store features that need root also break.
- **When it bites:** A regular user with sudo rights runs `make install-service` and starts the service. It runs as that user. Every OS package check fails at `apt-get update` (sudo refused). Auto-updates never install. Restarting services after updates fails and is reported as 'not running after the update'. The DDoS firewall rules cannot be applied. The service works only when installed from a real root shell, as on this host.
- **Fix:** Default the unit to root (do not take SUDO_USER implicitly), or make a non-root user an explicit opt-in flag. If a non-root user is chosen, omit NoNewPrivileges/RestrictSUIDSGID or warn that root-requiring features will not work.

#### 39. Update command timeouts do not work for wrapped/multi-process commands (runuser, sudo, sh -c scripts): the single worker can hang indefinitely

`updates-apps-3` · resource-leak · verifier: **confirmed** · [backend/internal/updates/exec.go:95](backend/internal/updates/exec.go#L95)

- **Status (2026-11-09):** ✅ fixed. Updates/exec.go Setpgid, kills the process group, WaitDelay 5s.
- **What:** Exec uses exec.CommandContext with bytes.Buffer/MultiWriter outputs. When the timeout fires, Go SIGKILLs only argv[0]. For git targets that is `runuser`, which forks the real git and waits on it. For release targets it is `sh -c` running a multi-line script that forks curl, git or pnpm. For a non-root monitor it is `sudo`. The orphaned child keeps the stdout/stderr pipe open, and with no cmd.WaitDelay, cmd.Run waits for EOF, so the timeout has no effect. All checks and updates go through one worker goroutine.
- **When it bites:** A git target owned by user `tautulli` does `runuser -u tautulli -- git fetch` with a 2-minute timeout. The network stalls (a TCP connection with no keepalive). At 2 minutes runuser is killed, but git keeps the pipe and Run blocks until the kernel gives up on the connection, which can take hours. Meanwhile `checking` stays true, every queued check and auto-update waits, and Tick skips auto-applies. The same happens with the qBittorrent recipe's `curl` (no --max-time) inside `set -e` scripts.
- **Fix:** Run the command in its own process group (SysProcAttr.Setpgid) and set cmd.Cancel to kill the whole group. Set cmd.WaitDelay so Wait returns soon after cancellation even if descendants hold the pipes.

#### 40. Restart or shutdown during an update exec()s over a running apt/dpkg after 1 minute, killing it with SIGPIPE

`updates-apps-2` · data-safety · verifier: **confirmed** · [backend/internal/updates/manager.go:426](backend/internal/updates/manager.go#L426)

- **Status (2026-11-09):** ✅ fixed. Main.go holds restarts while an update is applying; EXTEND_TIMEOUT to systemd.
- **What:** Wait(timeout) gives up after loop.Wait's fixed 1 minute (monitor/loop.go:75), while an update may run for up to SYSTEM_UPDATE_TIMEOUT (default 1h). The worker goroutine is never stopped, and nothing refuses or defers a restart while Applying() != "": api/settings.go:114 and api/backup.go:133 call Restart unconditionally. main.restart then calls syscall.Exec. The pipes Exec created for the child's stdout/stderr are O_CLOEXEC, so their read ends close at exec. The next write by apt-get or dpkg raises SIGPIPE and kills it in the middle of the transaction. The in-memory `Attempted` mark is also lost because finish() never ran.
- **When it bites:** The OS packages target is upgrading (apt-get -y upgrade, which takes several minutes). An admin saves a setting that needs a restart, or a manual self-update finishes. Updates.Wait returns after 60 s, db.Close runs, and syscall.Exec replaces the process. dpkg prints 'Setting up ...', gets SIGPIPE and dies. The host is left with 'dpkg was interrupted, you must manually run dpkg --configure -a' and half-configured packages. After the restart the auto-update retries and fails on the broken dpkg state.
- **Fix:** Defer a requested restart while m.Applying() != "" (or make Restart wait for the worker to go idle). Stop the worker from taking new jobs once shutdown begins. Run long package operations in their own transient unit (systemd-run, as apps does) or with stdout going to a file, so they survive a monitor exec or stop.

#### 41. Maintenance edit form keeps the first window's data when Edit is clicked on another window, so saving overwrites the wrong window

`fe-pages-admin-2` · frontend-state · verifier: **confirmed** · [frontend/src/pages/Maintenance.tsx:200](frontend/src/pages/Maintenance.tsx#L200)

- **Status (2026-11-09):** ✅ fixed. Maintenance.tsx keys WindowForm by id.
- **What:** WindowForm sets its state once with useState(initial) and is rendered without a `key`. The window list below the form still shows every Edit button while a form is open. Clicking Edit on a second window changes the `initial` prop, so the heading now reads 'Edit <B>'. The fields and `w.id` still belong to the first window, and submit calls api.saveMaintenance(w) with the first window's id. Services.tsx avoids this by keying CheckForm on the check's name; this form has no key.
- **When it bites:** The user clicks Edit on window A ('NAS update'), then Edit on window B ('Router reboot'). The form header says 'Edit Router reboot' but shows A's title, times and checks. The user changes the times to B's new slot and saves. Window A gets B's intended schedule and B is unchanged, so checks are silenced at the wrong time and the planned outage alerts.
- **Fix:** Render `<WindowForm key={editing.id || 'new'} ...>`, or hide or disable the per-row Edit buttons while a form is open.

#### 42. Setup's Plex step deletes the saved Plex token when it is not retyped

`fe-pages-admin-1` · data-safety · verifier: **confirmed** · [frontend/src/pages/Setup.tsx:144](frontend/src/pages/Setup.tsx#L144)

- **Status (2026-11-09):** ✅ fixed. Setup.tsx leaves PLEX_TOKEN out when not retyped.
- **What:** In PlexStep, values() always sends PLEX_TOKEN: useFound ? '@detected' : token. When a token is already saved (p.token_set) and the detected-token box is not used, the Save / 'Watch Plex' button stays enabled with an empty token field (line 174 allows `!token && p.token_set`). The test call passes because config.Layered skips empty values and falls back to the stored token. The save then sends PLEX_TOKEN: '', and Store.SaveSettings runs `DELETE FROM settings WHERE key = 'PLEX_TOKEN'` for an empty value. The qBittorrent step handles the same case correctly: it only adds the password when one was typed.
- **When it bites:** The Plex token was saved from the dashboard and Plex is remote, or its Preferences.xml is not found, so d.token_found is false. The user opens Setup and clicks 'Watch Plex', or 'Save' after changing the URL, without retyping the token. The test passes with the stored token and a 'Plex saved' toast appears. The dashboard row for PLEX_TOKEN is deleted and PLEX_MONITOR=true is stored. Plex monitoring then runs with no token, or with an older .env token, and fails.
- **Fix:** Leave PLEX_TOKEN out of the payload when useFound is false and the token field is empty, e.g. `...(useFound ? { PLEX_TOKEN: '@detected' } : token ? { PLEX_TOKEN: token } : {})`. Do the same for the test call.


### Low

#### 43. During shutdown, further signals are swallowed and background writers are not awaited before db.Close

`storage-config-main-12` · concurrency · verifier: **plausible** · [backend/cmd/nightjar/main.go:506](backend/cmd/nightjar/main.go#L506)

- **Status (2026-11-09):** ✅ fixed. Main.go stops catching signals after deciding; waits for background work before closing the db.
- **What:** signal.NotifyContext keeps capturing SIGINT/SIGTERM until the deferred cancel runs at the end of run(), so a second Ctrl+C during a slow shutdown does nothing. Loop.Wait can take up to a minute (Updates.Wait(time.Minute)) on top of check timeouts. Separately, the goroutines Plugins.Loop, Attacks.Run, Firewall.Start, MediaCleanup.Loop and report.Schedule are never joined. db.Close() runs while they may still be writing, and their SetMeta calls (attacks.history in OnEnd, mediacleanup.state) fail silently with ErrUnavailable. The goroutine at line 510 also uses select, so a restart request arriving together with SIGTERM can be chosen at random, and the process re-execs instead of exiting.
- **When it bites:** systemctl stop arrives while an automatic update is applying. Ctrl+C or a second SIGTERM is ignored. systemd's TimeoutStopSec=20 then SIGKILLs the process before the analytics flush and DB close. A media-cleanup pass that has just removed an item through Sonarr cannot persist its state after db.Close, so after the restart it re-notifies or retries that item.
- **Fix:** Call cancel() (unregistering the signal handlers) once stop is done, so a second signal kills the process. Give the background goroutines a WaitGroup joined before db.Close, and keep the shutdown budget under the unit's TimeoutStopSec.

#### 44. Blocking DNS lookups run inside the Defender's mutex

`ddos-firewall-9` · performance · verifier: **confirmed** · [backend/cmd/nightjar/main.go:804](backend/cmd/nightjar/main.go#L804)

- **Status (2026-11-09):** ✅ fixed. Main.go resolves trusted addresses in the background.
- **What:** Defender.Update and Unblock call d.trust() → d.Trusted() while holding d.mu. trustedAddresses resolves every remote server's hostname (up to 3s each, two names per host) once its 10-minute cache expires. Meanwhile Defender.View() blocks, and so do Watcher.View() and the /network API, along with the sampling loop.
- **When it bites:** During a volumetric attack the uplink is saturated and DNS times out. With 10 managed servers using hostnames, up to 20 × 3s = 60s pass inside d.mu every 10 minutes. The Network page hangs, attack sampling stops, and expiring blocks and new blocks are delayed for that minute.
- **Fix:** Resolve the trusted hostnames in a background goroutine on a timer and have Trusted() return only the cached slice, or call Trusted() before taking d.mu.

#### 45. ChangePassword writes back an admin snapshot taken before a slow hash, undoing concurrent 2FA and recovery-code changes

`api-auth-10` (also reported as x-security-5) · concurrency · verifier: **confirmed** · [backend/internal/accounts/accounts.go:201](backend/internal/accounts/accounts.go#L201)

- **Status (2026-11-09):** ✅ fixed. Accounts.go re-locks after hashing and changes only Hash.
- **What:** ChangePassword reads `ad := a.admin` under the lock, releases it, runs checkPassword and hashPassword (two PBKDF2 runs of 600k iterations each, roughly 0.5 to 1s), then saves `updated := *ad` built from the stale copy. Any admin change in between is overwritten: CheckCode consuming a recovery code or advancing LastStep, or EnableTwoFactor/DisableTwoFactor.
- **When it bites:** On another device the owner signs in with a recovery code while the password change is hashing. The change saves the old Recovery list, so the used recovery code becomes valid again and LastStep rolls back, which reopens TOTP replay. If 2FA was turned on during that window, the password change silently turns it off again (TOTP="").
- **Fix:** After hashing, take the lock and copy the current a.admin (`updated := *a.admin`), changing only Hash. Abort if the admin changed identity in the meantime.

#### 46. Session revocation can be undone: a concurrent Valid() writes the deleted session back to SQLite

`gap-4` · concurrency · verifier: **found by completeness critic (single reviewer, not independently re-verified)** · [backend/internal/accounts/accounts.go:276](backend/internal/accounts/accounts.go#L276)

- **Status (2026-11-09):** ✅ fixed. Accounts.go saves session touches under the lock.
- **What:** Valid() decides touch=true when a session was last seen over an hour ago. It updates Seen, copies the session, releases a.mu, and then calls a.store.SaveSession(copied), an INSERT … ON CONFLICT upsert. ChangePassword or EndSession can run in between, deleting the hash from the map and calling DeleteSessions. The late upsert then re-inserts the revoked row. The in-memory map no longer has it, but New() reloads every row from the DB at the next start. Dashboard actions trigger restarts routinely (settings save, backup restore). Separately, expired rows are deleted only when the same cookie is presented again, so the sessions table and map grow with every login.
- **When it bites:** An attacker holds a stolen session cookie and makes a request just as the owner changes the password to revoke it. Valid() passes the touch check and unlocks. ChangePassword deletes all other sessions. Valid's SaveSession re-inserts the stolen session. The owner saves a setting that triggers a restart, accounts.New reloads the row, and the stolen cookie works again for up to 30 days.
- **Fix:** Do the touch write while holding a.mu, or check under the lock that the session is still in the map before writing. Make it an UPDATE … WHERE hash=? rather than an upsert. Prune sessions with seen < now - SessionLife at startup and periodically.

#### 47. 2FA can be re-enrolled without the password, and /2fa/disable is an unthrottled password oracle

`api-auth-9` · security · verifier: **confirmed** · [backend/internal/accounts/twofactor.go:107](backend/internal/accounts/twofactor.go#L107)

- **Status (2026-11-09):** ✅ fixed. Twofactor.go Begin refuses when on; Enable needs the password; disable throttled.
- **What:** Turning two-factor off needs the password and a current code, but BeginTwoFactor and EnableTwoFactor need only a session. They replace an existing TOTP secret and all recovery codes without any proof of the old factor. Separately, the disableTwoFactor handler (api/auth.go:196) never calls recordFailure, while DisableTwoFactor reports ErrWrongCurrent ('the current password is not right') for a wrong password before it checks the code.
- **When it bites:** Someone holding a stolen or left-open session cookie calls /2fa/begin and /2fa/enable with their own authenticator. The owner's authenticator and recovery codes stop working, so the owner can no longer sign in once the cookie expires, until a CLI reset. The same session can also brute-force the password through POST /api/auth/2fa/disable with any code. It is never throttled, and the error text says whether the password was right.
- **Fix:** Require the password (and the current code when 2FA is already on) in EnableTwoFactor/BeginTwoFactor. Call recordFailure for ErrWrongCurrent and ErrCode in the disable handler, as changePassword does. Consider ending other sessions when the factors change.

#### 48. Agent server WriteTimeout (10s) is shorter than its firewall operations (iptables -w calls up to 20s each)

`remote-agent-3` · correctness · verifier: **plausible** · [backend/internal/agent/agent.go:315](backend/internal/agent/agent.go#L315)

- **Status (2026-11-09):** ✅ fixed. Agent/firewall.go sets a 45s write deadline on firewall routes.
- **What:** The agent's http.Server sets `WriteTimeout: 10 * time.Second` for every route. Only the update handler extends its deadline. The firewall handlers (firewall.go:113-232) run firewall.Manager operations that call `iptables(-restore) -w` and wait on the xtables lock, each with a 20s timeout (internal/firewall/firewall.go:195), and then run view() (Status), which runs more commands. The dashboard side deliberately allows 30s for these calls (remote/firewall.go:91-101, "a change may wait on iptables' lock"). Once a handler passes 10s, the agent cannot write its response, so the dashboard reports a connection error while the change has gone through, or is still going through, on the server.
- **When it bites:** During an attack, the agent's own DDoS defender, Docker or fail2ban holds the xtables lock for a few seconds. The admin adds a block rule or changes the default policy for that server in the dashboard. iptables-restore -w waits about 8s and Status takes a few more seconds. Past 10s the agent's write fails, and the dashboard shows "no answer from <server>: EOF" although the rule (or a DROP default) is now active, so the admin retries or assumes the change failed.
- **Fix:** In the firewall/defense handlers, extend the deadline with http.NewResponseController(w).SetWriteDeadline(time.Now().Add(45*time.Second)) as updateHandler does, or raise the server's WriteTimeout above the dashboard's 30s.

#### 49. Concurrent update requests race on the same .new file and on Self.Hash

`remote-agent-5` · concurrency · verifier: **confirmed** · [backend/internal/agent/update.go:105](backend/internal/agent/update.go#L105)

- **Status (2026-11-09):** ✅ fixed. Agent/update.go Apply holds s.mu, writes a unique temp file, fsyncs, renames.
- **What:** Apply writes `s.Path + ".new"` with os.WriteFile (O_TRUNC) and renames it, with no lock. The hash and ELF checks run on the in-memory bytes, not the file. Two concurrent authorized POSTs to /v1/update interleave their truncates and writes on the same path, and the first rename installs a mixed file. Apply also writes s.Hash while the readings handler reads self.Hash (agent.go:261) with no synchronization, which is a data race.
- **When it bites:** Two dashboards on different builds share an agent token, or the same agent is added twice under its IP and its hostname, and both push an update at once. Writer B truncates .new while writer A is mid-write, and A's rename installs a corrupted binary. On the os.Exit restart, systemd cannot start the agent and it needs a manual reinstall over SSH.
- **Fix:** Serialize Apply with a mutex (or reject an update while one is in progress), write to a unique os.CreateTemp file in the same directory, fsync it before the rename, and guard Hash with the mutex or an atomic.

#### 50. Auto-update kills an agent that is not run under systemd

`remote-agent-4` · correctness · verifier: **confirmed** · [backend/internal/agent/update.go:152](backend/internal/agent/update.go#L152)

- **Status (2026-11-09):** ✅ fixed. Agent/update.go restart() exits only under systemd, otherwise execs the new build.
- **What:** After applying a build, the default Restart is `os.Exit(0)`, which assumes systemd (Restart=always) will start the new build. CanUpdate only checks that the binary's directory is writable, and Readings report CanUpdate=true over HTTPS, so the dashboard (remote/update.go:79-83) pushes an update to any HTTPS agent whose directory is writable, including one started in the foreground as the usage text documents (`nightjar-agent [-listen :9101]` with AGENT_TOKEN).
- **When it bites:** A user runs `AGENT_TOKEN=... ./nightjar-agent` from ~/bin under tmux or nohup. When the dashboard is upgraded, the next poll sees a different build, sends the new build, and the agent swaps its file and exits with status 0. Nothing restarts it, so the server goes offline in the dashboard until someone restarts it by hand.
- **Fix:** Report CanUpdate=true only when running under the installed unit (for example, Self.Path == ExePath and INVOCATION_ID set by systemd), or re-exec the new binary with syscall.Exec instead of exiting when not supervised.

#### 51. Fixed gate recovery window shorter than a check's cadence resets every episode, disabling cooldown/backoff for slow checks

`monitor-core-6` · correctness · verifier: **confirmed** · [backend/internal/alerts/gate.go:43](backend/internal/alerts/gate.go#L43)

- **Status (2026-11-09):** ✅ fixed. Alerts/gate.go GetEvery sets a recovery floor of 3x the interval.
- **What:** Gate.ShouldAlert ends an episode when now-lastSeen > recovery. Recovery is set once at startup as max(ALERT_RECOVERY_SECONDS, 3*CHECK_INTERVAL) (cmd/nightjar/main.go:300). Several gated conditions are re-evaluated less often than that: SMART drive temperature every SMART_CHECK_INTERVAL (default 1800s), uptime checks with interval > 300s (raiseAlert per failing result), and every per-cycle gate after CHECK_INTERVAL is raised live from the dashboard (CHECK_INTERVAL is a live key but the gates keep the startup value). Each evaluation then starts a new episode, so the escalating cooldown never applies.
- **When it bites:** An uptime check with a 600s interval is down overnight. Each failing result resets the gate and sends 'X is down' every 10 minutes for hours, instead of backing off to ALERT_MAX_COOLDOWN (3600s). The same happens with 'Drive running hot' every 30 min. If CHECK_INTERVAL is changed to 400s in the dashboard, CPU and memory alerts fire on every cycle.
- **Fix:** Make recovery relative to the caller's cadence: pass an expected interval per key, or end an episode only when the condition was explicitly observed clear (add a Clear(key) call when the condition is false). Rebuild or update the Gates parameters when live settings change.

#### 52. alerts.Gate is mutated without synchronization from multiple goroutines

`monitor-core-7` · concurrency · verifier: **confirmed** · [backend/internal/alerts/gate.go:89](backend/internal/alerts/gate.go#L89)

- **Status (2026-11-09):** ✅ fixed. Alerts/gate.go has its own mutex; Allow is atomic.
- **What:** Gates.Get locks s.mu only while it looks up the gate. ShouldAlert/MarkSent then mutate the returned *Gate with no lock, from the loop, from serviceChecks report goroutines (services.go:39-50, :85) and from the driveWatcher goroutine (system.go:394). Get's prune loop (len >= 256, easily reached with per-PID 'proc:' keys) reads g.seen/g.lastSeen of every gate under s.mu while those goroutines write them. services.Monitor.Record clears st.running (services/monitor.go:153) before report() runs, so the next Claim can start a second check of the same service while the previous report is still in raiseAlert, and both then use the same 'service:X' gate at once.
- **When it bites:** With more than 256 gates, a prune in the loop goroutine races with a service report goroutine writing lastSeen (go test -race would flag it in production). When a slow Discord send overlaps the next check of the same service, both goroutines can pass ShouldAlert before either calls MarkSent, and two 'down' alerts go out.
- **Fix:** Give Gate its own mutex, or expose an atomic Gates.Allow(key, now) that does ShouldAlert+MarkSent under s.mu. Have prune use the same lock.

#### 53. Analytics "peak" memory and latency are maxima of bucket averages, not real peaks

`storage-config-main-11` · correctness · verifier: **confirmed** · [backend/internal/analytics/analytics.go:257](backend/internal/analytics/analytics.go#L257)

- **Status (2026-11-09):** ✅ fixed. Analytics stores per-minute memMax/latencyMax and SampleBuckets uses them.
- **What:** summarize computes CPU and temperature peaks from the stored per-minute maxima (cpu_max, temp_max). MemPeak and LatencyPeak instead take maxOf over p.Mem and p.Latency. SampleBuckets returns those as AVG(mem) and AVG(latency) per bucket (30 min on the 7d range, 2 h on 30d), so the reported peak is the highest bucket average.
- **When it bites:** Memory jumps to 98% for 10 minutes during a backup and sits at 50% otherwise. On the 30-day view the 2-hour bucket average is about 54%, so the dashboard shows "Memory peak 54%" while the 24h view showed 98%. Latency spikes are smoothed away the same way.
- **Fix:** Store mem_max and latency_max per minute, as is done for cpu and temp, and select MAX() of them in SampleBuckets. Or rename these fields so they do not claim to be peaks.

#### 54. Activity log skips any change whose path ends in /test, /try, /detect or /rescan, so actions on a check named 'test' are never recorded

`x-contract-7` · correctness · verifier: **confirmed** · [backend/internal/api/activity.go:92](backend/internal/api/activity.go#L92)

- **Status (2026-11-09):** ✅ fixed. Api/activity.go skipped is keyed by route pattern.
- **What:** logged() decides whether to record a change with strings.HasSuffix(r.URL.Path, p) over the skipped list, applied to the decoded request path rather than to the route pattern. User-chosen path values (check names, update target ids, unit names) can end in these suffixes.
- **When it bites:** The admin, or someone holding the API token, runs DELETE /api/checks/test, PUT /api/checks/test or POST /api/checks/test/repair. The path ends in "/test", so nothing is written to the activity log, and the audit trail silently misses the deletion or change of that check.
- **Fix:** Match on r.Pattern, e.g. a set of skipped patterns such as "POST /api/checks/test" and "POST /api/plugins/{id}/try", instead of suffixes of the concrete path.

#### 55. Login throttle is check-then-act around a 600k-iteration PBKDF2, so concurrent requests bypass it and burn CPU

`api-auth-8` · security · verifier: **confirmed** · [backend/internal/api/auth.go:72](backend/internal/api/auth.go#L72)

- **Status (2026-11-09):** ✅ fixed. Api/server.go passwordTry reserves the failure before hashing; caps concurrent PBKDF2 at 2.
- **What:** readCredentials checks s.throttled(ip) once at the start. recordFailure runs only after Accounts.Verify finishes its PBKDF2-SHA256 at 600,000 iterations (hundreds of ms of CPU). Nothing limits concurrent sign-ins, so all requests in flight pass the check before any failure is recorded, and each one costs a full key derivation.
- **When it bites:** An unauthenticated client opens 500 concurrent POST /api/auth/login requests. All pass the throttle (0 recorded failures), so 500 password guesses go through in one burst instead of 10 per minute, and the PBKDF2 work pins every core for minutes. The monitor loop and dashboard slow down and the watchdog may fire. Repeating the burst every minute keeps it going.
- **Fix:** Record or reserve an attempt before verifying (increment, then roll back on success), and cap concurrent password verifications with a small semaphore (for example 2), returning 429 when it is full.

#### 56. Public /api/status rebuilds 30 days of incidents on every request without caching, holding the global store mutex

`api-auth-5` (also reported as x-contract-8) · performance · verifier: **confirmed** · [backend/internal/api/incidents.go:156](backend/internal/api/incidents.go#L156)

- **Status (2026-11-09):** ✅ fixed. Api/incidents.go publicNotices returns early; incident ends cached for 1 min.
- **What:** statusPage (unauthenticated, polled by every visitor) calls publicNotices, which calls s.listIncidents(30) on every request. That runs NotificationsBetween (up to 20000 rows), SampleBuckets over 30 days of samples (a GROUP BY aggregate), AllFeedback/LearnedFrom and incidents.Build. Each storage call takes Store.mu, the single mutex that serializes all database access, including the monitor loop's writes. checkDays is cached for 5 minutes, but this path is not, and it runs even when there are no published notices.
- **When it bites:** Anyone on the internet requests GET /api/status in a loop, or the page is simply popular. Each request does heavy SQLite work under the global lock, so the monitoring loop's sample and check writes queue behind it. The loop heartbeat stalls, which can trigger the freeze watchdog and false 'metrics stalled' alerts, and dashboard API latency rises sharply. No authentication or rate limit applies.
- **Fix:** Return early when no published notice exists, and look up only the ended times of published notice IDs. Otherwise cache the public payload (or the incident list) for 30 to 60 seconds, as dayCache does.

#### 57. auth() returns 429 to valid sessions once their IP bucket has 10 token failures, letting anyone who shares the bucket lock out the admin

`api-auth-2` · security · verifier: **plausible** · [backend/internal/api/server.go:416](backend/internal/api/server.go#L416)

- **Status (2026-11-09):** ⚪ accepted risk. A valid session cookie is let in before the throttle, so the dashboard cannot be locked out. A correct bearer token is still refused while its address is locked out: accepting it would give guessers an unthrottled yes/no answer. Scripts should reach Nightjar from an address untrusted clients do not share.
- **What:** auth() calls s.throttled(ip) before it checks credentials, and the failures map is shared by every client that resolves to the same clientIP. Behind Cloudflare plus a local nginx that appends $remote_addr, the last X-Forwarded-For hop is a Cloudflare edge IP. Behind a proxy that sets no forwarding header at all, everyone resolves to 127.0.0.1. Either way, unrelated clients share one bucket. A signed-in owner with a perfectly valid cookie is refused too.
- **When it bites:** An unauthenticated attacker sends 10 requests per minute to any authenticated route (GET /api/stats) with `Authorization: Bearer x`. Every one counts through recordFailure against the shared proxy or edge IP. From then on every dashboard request from the owner returns 429 Too Many Requests, and wrong logins against the same bucket keep the lockout going indefinitely.
- **Fix:** Check valid credentials (session cookie or correct token) before applying the throttle, and throttle only failed attempts. Alternatively, key the limiter per presented credential or account as well as per IP.

#### 58. Public /health resets gopsutil's global per-CPU baseline that the monitor loop's CPU readings depend on

`api-auth-11` · correctness · verifier: **confirmed** · [backend/internal/api/server.go:504](backend/internal/api/server.go#L504)

- **Status (2026-11-09):** ✅ fixed. /health uses Latest().CPUPercent, no PerCore call.
- **What:** collect.PerCore is cpu.Percent(0, true), which compares against a process-wide 'last call' snapshot inside gopsutil. The monitor loop (monitor/resources.go:32) and the unauthenticated /health handler both call it, so each health request resets the baseline the loop's next reading measures from. Over very short windows gopsutil returns 0 when busy time has not advanced and 100 when only busy time advanced.
- **When it bites:** An uptime probe, or anyone, polls /health many times a second. The loop's per-core readings then cover millisecond windows and quantize to 0 or 100. The CPU average and history become noise, and CPU-threshold or temperature-cause alerts fire or are missed at random.
- **Fix:** Serve /health CPU from s.ctx.Latest() (already computed by the loop) instead of calling PerCore. If a fresh reading is needed, use a private baseline: cpu.Times plus your own last snapshot.

#### 59. Status page designs within the per-field limits are rejected by the 64 KiB body cap

`gap-2` · api-contract · verifier: **found by completeness critic (single reviewer, not independently re-verified)** · [backend/internal/api/statuspage.go:41](backend/internal/api/statuspage.go#L41)

- **Status (2026-11-09):** ✅ fixed. PUT /api/statuspage allows up to 2 MiB and answers 413 clearly.
- **What:** StatusDesign.validate allows CSS up to 50,000 characters and header and footer HTML up to 20,000 each, plus 30 groups of up to 200 items. The designer shows matching counters (StatusDesigner.tsx:130-142). But PUT /api/statuspage goes through writable() → fromDashboard, which wraps the body in http.MaxBytesReader(64<<10) (writes.go:9,31). Any design over 64 KiB after JSON encoding fails json.Decode, and the handler answers 400 'Expected a status page design'.
- **When it bites:** The owner pastes 40,000 characters of CSS and 15,000 characters each of header and footer HTML. All three counters are green. Save returns 'Expected a status page design', and the design cannot be saved until content is cut, with no hint that total size is the problem.
- **Fix:** Give the status page route its own larger cap (as backup restore does), above maxCSS + 2*maxHTML plus JSON overhead. Or lower the per-field limits so their sum fits in 64 KiB, and return a 413 with a clear message when MaxBytesReader trips.

#### 60. CSRF Origin check compares against r.Host only, so login and all writes fail behind a proxy that rewrites Host

`api-auth-12` · correctness · verifier: **plausible** · [backend/internal/api/writes.go:22](backend/internal/api/writes.go#L22)

- **Status (2026-11-09):** ✅ fixed. Api/writes.go sameOrigin accepts X-Forwarded-Host from loopback, DASHBOARD_URL, Sec-Fetch-Site.
- **What:** fromDashboard, and the inline copy in the backup restore route, reject any request whose Origin host differs from r.Host. Browsers send Origin on same-origin POST, PUT and DELETE fetches. nginx's default proxy_pass sends `Host: $proxy_host` (for example 127.0.0.1:8080) unless `proxy_set_header Host $host` is added, and X-Forwarded-Host is ignored. Every write, including /api/auth/login and /api/auth/setup, then returns 403 'Cross-origin changes are not allowed'.
- **When it bites:** A user puts Nightjar behind a minimal nginx location block (proxy_pass http://127.0.0.1:8080;). The sign-in screen loads, but every sign-in attempt returns 403 Cross-origin, and the admin cannot create the account or sign in.
- **Fix:** Also accept Origin matching DASHBOARD_URL's host or X-Forwarded-Host when the request comes from loopback (a trusted proxy). When it does not match, return an error that explains the proxy Host header problem.

#### 61. App install wipes an existing /opt/<id> that the 'installed elsewhere' check missed

`updates-apps-11` · data-safety · verifier: **confirmed** · [backend/internal/apps/script.go:68](backend/internal/apps/script.go#L68)

- **Status (2026-11-09):** ✅ fixed. Apps/script.go moves unknown /opt/<id> and /var/lib/<id> aside instead of deleting.
- **What:** For archive apps the install script runs `rm -rf /opt/<id>` unconditionally. The guard in Install (manager.go:230) only looks for a store manifest, a swizzin lock, one of a few fixed unit-file names, or a process currently running under the binary's name (with active units passed as nil). A stopped install whose unit has another name, or a stopped Docker/hand-run copy, is not detected. The script also chowns a pre-existing /var/lib/<id> to the store user, and a later purge deletes it.
- **When it bites:** A user has Sonarr in /opt/sonarr run by a hand-made unit `sonarr4.service`, which is stopped at the moment. They click Install on Sonarr in the store. Status reports not installed, the script runs `rm -rf /opt/sonarr`, and the existing installation is replaced. A pre-existing /var/lib/sonarr is taken over and chowned, and removing the store app with purge deletes it.
- **Fix:** Refuse (or move aside to a timestamped backup) when /opt/<id> or /var/lib/<id> exists and there is no store manifest.

#### 62. App-store reinstall runs setup as root inside the app user's data directory and follows that user's symlinks

`gap-3` · security · verifier: **found by completeness critic (single reviewer, not independently re-verified)** · [backend/internal/apps/script.go:74](backend/internal/apps/script.go#L74)

- **Status (2026-11-09):** ✅ fixed. Apps/script.go setup snippets run as the app user.
- **What:** The first install ends with `chown -R user:user /opt/<id> /var/lib/<id>` (script.go:76), so the service account owns the data directory. A reinstall is allowed when By=='store' (manager.go Install). It runs the app's setup snippet as root before the chown. For servarr apps this is `[ -f {{data}}/config.xml ] || printf '<Config>…' > {{data}}/config.xml` (catalog.go:53); navidrome uses navidrome.toml (catalog.go:171). The `-f` test and the `>` redirect both follow symlinks the unprivileged app user planted.
- **When it bites:** Sonarr (a web-exposed app running as user 'media') is compromised. The attacker replaces /var/lib/sonarr/config.xml with a dangling symlink to /etc/nologin. The admin later clicks Reinstall in the app store. As root, the setup step sees no regular file and writes the XML through the symlink, creating /etc/nologin, which blocks every non-root login, including the admin's SSH user. The same trick can create any other root-owned file that does not exist yet, with fixed content.
- **Fix:** Run setup snippets as the app user (runuser -u <user> -- sh -c ...). Or test with `[ -e ] || [ -L ]` and write with a no-follow, exclusive create (set -C / noclobber, or write to a temp file and mv -T). Or make the data directory root-owned with only subdirectories handed to the app user.

#### 63. Forced journal vacuum is less aggressive than the normal one, and a small threshold vacuums to 0M

`host-collect-18` · correctness · verifier: **confirmed** · [backend/internal/cleanup/logs.go:371](backend/internal/cleanup/logs.go#L371)

- **Status (2026-11-09):** ✅ fixed. Cleanup/logs.go forced vacuum is clamped.
- **What:** vacuumJournal uses the threshold size ("%.0fM") normally but a fixed "100M" when force=true (emergency or aggressive). With LOG_THRESHOLD_MB below 100, the emergency path keeps more journal than routine trimming does. With LOG_THRESHOLD_MB below 0.5 (the minimum is 1, but fractional values are accepted), the size formats as "0M", which deletes all archived journals.
- **When it bites:** With LOG_THRESHOLD_MB=50 and a full disk, emergency cleanup vacuums the journal to 100M while routine cleanup would vacuum to 50M, so the emergency frees less space than a normal pass.
- **Fix:** Use min(threshold, 100M) when forced, and clamp the size to at least 1M.

#### 64. Partitions are deduplicated by mountpoint, not device, so btrfs subvolumes and bind mounts are counted twice in TotalDisk

`host-collect-16` · correctness · verifier: **confirmed** · [backend/internal/collect/host.go:157](backend/internal/collect/host.go#L157)

- **Status (2026-11-09):** ✅ fixed. Collect/host.go dedupes partitions by device.
- **What:** Partitions deduplicates on p.Mountpoint only. A btrfs filesystem mounted as / and /home (the default on Fedora and openSUSE), or a bind mount of a data disk, appears twice, and each entry reports the full filesystem's total and used space. TotalDisk adds them, and s.DiskPercent = TotalDisk.Percent decides when disk cleanup and emergency cleanup run.
- **When it bites:** A 1 TB btrfs root with / and /home subvolumes plus a 4 TB data disk: total is computed as 6 TB with the btrfs usage doubled. This skews the aggregate percent that drives cleanup and alerts, and the dashboard shows 6 TB of storage.
- **Fix:** Deduplicate by device (and by fs ID for btrfs, from statfs f_fsid) when building the list that feeds TotalDisk.

#### 65. DiskIO sums whole disks, partitions and dm devices, double-counting throughput

`host-collect-15` · correctness · verifier: **confirmed** · [backend/internal/collect/host.go:223](backend/internal/collect/host.go#L223)

- **Status (2026-11-09):** ✅ fixed. Collect/host.go DiskIO sums whole devices only.
- **What:** gopsutil disk.IOCounters() returns every /proc/diskstats entry, including sda and sda1..n, dm-* on top of their physical volume, md arrays and their members, and loop devices. DiskIO adds them all.
- **When it bites:** A 100 MB/s write to an LVM volume on /dev/sda2 is counted for dm-0, sda2 and sda, so the temperature alert and the freeze monitor's metrics report about 300 MB/s.
- **Fix:** Sum only whole physical devices: skip partitions (entries with /sys/block/<dev> missing, or a 'partition' file present), and skip dm-*, md* and loop*, or count only leaf devices.

#### 66. Process handles cached by PID keep the old process's name after PID reuse

`host-collect-20` · correctness · verifier: **plausible** · [backend/internal/collect/processes.go:269](backend/internal/collect/processes.go#L269)

- **Status (2026-11-09):** ✅ fixed. Collect/processes.go replaces stale cached handles.
- **What:** ProcessScanner reuses process.Process handles keyed only by PID across scans. gopsutil caches the name in the handle (NameWithContext returns p.name once set), so when a PID is reused between scans the new process is reported under the dead process's name. Its CPU percent is also measured against the old process's CPU times.
- **When it bites:** A short-lived 'sshd' child exits and its PID is reused by a CPU-hungry process before the next scan. The process alert and dashboard report 'sshd' at 100% CPU, and temperature.go's name matching misattributes the load.
- **Fix:** Store the process create time with the handle and replace the handle when CreateTime() differs.

#### 67. LoadDotenv puts every secret into the process environment, and repair and app scripts inherit it

`storage-config-main-13` · security · verifier: **plausible** · [backend/internal/config/env.go:40](backend/internal/config/env.go#L40)

- **Status (2026-11-09):** ✅ fixed. Config/env.go strips secret keys from child script environments.
- **What:** LoadDotenv calls os.Setenv for every key in .env, including WEBHOOK, WEB_AUTH_TOKEN, SMTP_PASSWORD, PLEX_TOKEN, GITHUB_TOKEN and the *_API_KEY values. Plugins (explicit env) and updates (passthrough list) scrub their child environment, but repair scripts get `append(os.Environ(), ...)` (repair/repair.go:184). App-store install scripts started without systemd-run inherit the full environment by default (apps/manager.go:469).
- **When it bites:** A repair script, or a catalog app's install script on a host without systemd-run, runs `env` in its output or calls a third-party installer that collects environment data. WEB_AUTH_TOKEN (full API access), Discord webhooks and the SMTP password then show up in repair output (stored in the repairs table and shown on the dashboard) or leave the machine.
- **Fix:** Keep .env values in a private map that config reads, rather than os.Setenv. Or give repair and app scripts a minimal allowlisted environment, as updates.Exec does.

#### 68. Number settings accept NaN/Inf; CHECK_INTERVAL=NaN makes the monitoring loop spin without sleeping

`storage-config-main-2` · correctness · verifier: **confirmed** · [backend/internal/config/fields.go:106](backend/internal/config/fields.go#L106)

- **Status (2026-11-09):** ✅ fixed. Config/fields.go number() rejects NaN/Inf.
- **What:** number() parses with strconv.ParseFloat, which accepts "NaN", "Inf", "+Inf" and "infinity". bounds() only tests `v < min` and `v > max`. Both comparisons are false for NaN, so NaN passes any bounds, and +Inf passes every field that has no max. config.Validate (used by the settings API and by backup restore) therefore accepts these values. LoadFrom stores them. I confirmed this with an overlay test: Validate({CHECK_INTERVAL: NaN, ALERT_COOLDOWN: +Inf, CPU_THRESHOLD: nan}) returns no problems. On amd64, time.Duration(NaN*1e9) and time.Duration(+Inf*1e9) both come out as -2562047h (int64 min).
- **When it bites:** An admin (or a restored backup) sets CHECK_INTERVAL to "NaN". It is a live key, so it takes effect at once. loop.go:87 then waits on `time.After(time.Duration(NaN*1e9))`, a negative duration that fires immediately, so the monitoring loop runs cycles back to back at 100% CPU, with every collector, check scheduler and gate running continuously. ALERT_COOLDOWN=Inf likewise turns into a negative cooldown. A threshold set to NaN (e.g. CPU_THRESHOLD) silently disables its alert, because every comparison is false. JSON encoding of any NaN setting value also fails in API responses.
- **Fix:** In number(), reject values where math.IsNaN(v) || math.IsInf(v, 0) ("%q is not a number") before the bounds check.

#### 69. Applied rules are cached and never re-verified, so a flushed INPUT silently disables the defense and the firewall rules

`ddos-firewall-8` · correctness · verifier: **confirmed** · [backend/internal/ddos/defend.go:310](backend/internal/ddos/defend.go#L310)

- **Status (2026-11-09):** ✅ fixed. Ddos/defend.go and firewall.go re-check the INPUT jump every minute.
- **What:** syncLocked returns early when the generated rules equal d.applied. It does not check that the INPUT jump or the chain contents still exist. firewall.Manager only reapplies on a change or a restart. Any tool that reloads the ruleset (netfilter-persistent reload, `iptables-restore < rules.v4` without --noflush, an iptables-backend firewalld reload) removes the jumps. Both components then keep reporting themselves as active.
- **When it bites:** During an attack, an admin runs `netfilter-persistent reload`. The NIGHTJAR jump disappears, but View() still shows Active with all measures and blocks, so the defense is silently off. Likewise, a dashboard 'block' rule or default drop stops working, while the Firewall page still lists it and shows hit counters (now zero).
- **Fix:** On each sample, or in Status(), run `iptables -C INPUT -j <chain>` (cheap). If the jump is missing, clear d.applied (or reapply in the Manager) so the rules are put back.

#### 70. Per-address blocking runs for every attack type, so legitimate heavy web clients are blocked during an unrelated UDP/SYN/volumetric attack

`ddos-firewall-2` · correctness · verifier: **confirmed** · [backend/internal/ddos/defend.go:435](backend/internal/ddos/defend.go#L435)

- **Status (2026-11-09):** ✅ fixed. Ddos/defend.go blocks only on detected floods, each gated by its method.
- **What:** Update() calls blockLocked() whenever s.Attacking is true. blockLocked always applies all three criteria (100 half-open, 300 connections, and >=30 web requests/s with >=5% of all requests), whatever methods were detected. The 'http_blocks' measure, which tells the user that busy web clients get blocked, is only added for HTTPFlood. In a non-HTTP attack, s.Requests is ordinary traffic, so the 5% condition is trivially met by one active visitor.
- **When it bites:** A UDP flood or volumetric attack is under way and nginx sees about 50 req/s in total. A real user opens a media or admin web UI that loads 150+ thumbnails or assets in one 5s interval (>=30 req/s, far above 5% of the total). The user's IP is blocked for 15 minutes in the firewall or in nginx. Nothing in the dashboard's measures says web clients are being blocked.
- **Fix:** Gate each criterion on the matching detected method: requests only when HTTPFlood is in s.Methods, Established only when ConnectionFlood is present, SynRecv only when SYNFlood is present. When HTTPFlood is present, also measure the share against attack-level traffic.

#### 71. IPv6 attackers bypass per-address limits and fill the block table, because limits and blocks are per /128

`ddos-firewall-10` · security · verifier: **confirmed** · [backend/internal/ddos/defend.go:497](backend/internal/ddos/defend.go#L497)

- **Status (2026-11-09):** ✅ fixed. Ddos/defend.go IPv6 masks /64, blocks keyed per /64.
- **What:** The SYN/UDP hashlimits use `--hashlimit-mode srcip` with the default IPv6 srcmask of /128, connsOver uses `--connlimit-mask 128`, and blocks are per single address. An IPv6 attacker normally controls at least a /64, so each per-address limit can be multiplied without bound. Also, blockLocked stops adding new blocks once maxBlocks (2000) is reached (line 449), for every family.
- **When it bites:** A bot with a /64 sends a SYN flood from rotating addresses. No single address goes over 50 SYN/s or 300 connections, so nothing is limited. If addresses do get blocked, 2000 rotating /128s fill d.blocked and no other attacker, IPv4 included, can be blocked until those expire.
- **Fix:** For IPv6 use `--hashlimit-srcmask 64` and `--connlimit-mask 64`, and aggregate blocks to /64 (key d.blocked by the /64 prefix for v6).

#### 72. Outbound connections are counted as attack sources, and blocks drop their replies

`ddos-firewall-6` · correctness · verifier: **confirmed** · [backend/internal/ddos/read.go:122](backend/internal/ddos/read.go#L122)

- **Status (2026-11-09):** ✅ fixed. Ddos/read.go counts ESTABLISHED only on listening ports; conntrack RETURN before DROPs.
- **What:** ParseTCP counts every ESTABLISHED socket by remote address and never checks whether this machine started the connection. Connections this host opens to an upstream (reverse-proxy keepalive pools, object-storage mounts, API backends) are counted as that remote's 'Established'. The defender's block rule (`-A NIGHTJAR -s X -j DROP`) sits before any conntrack ESTABLISHED accept, so it also drops replies to this host's own connections. MaxPerSource >= 300 also declares a ConnectionFlood by itself (detect.go:179).
- **When it bites:** nginx proxies to a backend on a public IP with a keepalive pool of 300+ connections, or rclone/s3fs holds many parallel connections to one endpoint. That alone triggers 'Connection flood' with 300 from one address. blockLocked then blocks the upstream's IP for 15 minutes, and the site or mount breaks because all reply packets are dropped.
- **Fix:** Count only sockets whose local port is a listening port (read the LISTEN entries first), or skip sockets whose local port is in the ephemeral range. Alternatively, put an ESTABLISHED,RELATED RETURN before the per-address DROP lines for addresses this host connected to.

#### 73. Defense chains and firewall chains are never removed when the daemon stops or the agent is uninstalled

`ddos-firewall-7` · resource-leak · verifier: **confirmed** · [backend/internal/ddos/watch.go:135](backend/internal/ddos/watch.go#L135)

- **Status (2026-11-09):** ✅ fixed. Ddos/watch.go stops the defender on shutdown; agent uninstall removes rules.
- **What:** Watcher.Run returns on ctx.Done() without asking the Defender to remove its NIGHTJAR/NIGHTJAR-UDP chains or the nginx deny file. Cleanup only happens at the next Start. Separately, agent.Uninstall (internal/agent/install.go:178-193) deletes the state dir, including firewall.json, but leaves NIGHTJAR-RULES/NIGHTJAR-OUT (possibly with default DROP) and the defender's chains in iptables until reboot.
- **When it bites:** Nightjar is stopped mid-attack, or the agent is uninstalled while 'Drop UDP to closed ports' is active. NIGHTJAR-UDP keeps dropping new UDP to every port that was not open at that moment, so a WireGuard or game server started later silently gets nothing, and blocked addresses stay blocked indefinitely. After an agent uninstall with default "drop" kept, any new service on that host stays unreachable until reboot, with no tool left to show or remove the rules.
- **Fix:** Add a Defender.Stop(ctx) that runs removeLocked plus writeWebLocked(""), and call it from Run on ctx.Done() using a non-cancelled context. In agent Uninstall, remove the jumps and flush/delete NIGHTJAR, NIGHTJAR-UDP, NIGHTJAR-RULES and NIGHTJAR-OUT in both families.

#### 74. Unbounded per-attack source map, re-sorted on every sample

`ddos-firewall-11` · resource-leak · verifier: **confirmed** · [backend/internal/ddos/watch.go:431](backend/internal/ddos/watch.go#L431)

- **Status (2026-11-09):** ✅ fixed. Ddos/watch.go prunes sources past maxSources.
- **What:** record() adds up to 10 new addresses to w.sources on every sample for the life of an attack and never prunes them. It then copies and sorts the whole map each sample to keep the top 10.
- **When it bites:** A multi-day attack from a rotating botnet adds about 7,200 entries an hour (≈170k a day). Memory and per-sample CPU (an O(n log n) sort of all entries every 5s) keep growing until the attack ends.
- **Fix:** Cap w.sources, for example by evicting the lowest-weight entries once it passes a few thousand, or keep only a bounded top-N structure.

#### 75. Discord interactions endpoint has no replay protection (timestamp freshness or interaction-id dedupe)

`x-security-6` (also reported as notify-incidents-9) · security · verifier: **confirmed** · [backend/internal/discord/bot.go:138](backend/internal/discord/bot.go#L138)

- **Status (2026-11-09):** ✅ fixed. Discord/bot.go rejects stale timestamps and dedupes interaction IDs.
- **What:** Interactions checks the Ed25519 signature over timestamp+body but never compares X-Signature-Timestamp with the current time and never remembers interaction IDs. A captured signed request stays valid forever and re-runs its action each time it is sent: restart:<unit> (systemctl restart), repair:<check> (runs a repair script), keep:<key>, mute.
- **When it bites:** A TLS-terminating proxy or CDN logs request bodies and headers, or an attacker otherwise gets one signed button click for "Restart plexmediaserver" or "Run its repair". Replaying that POST to /discord/interactions restarts the service or runs the repair script again on every replay, at will, with no Discord user involved.
- **Fix:** Reject requests whose X-Signature-Timestamp is more than about 5 minutes from now. Keep a short-lived set of processed interaction IDs (the body's "id") and ignore duplicates.

#### 76. Classic embed fallback ignores Discord's 6000-character total embed limit

`notify-incidents-8` · correctness · verifier: **confirmed** · [backend/internal/discord/client.go:241](backend/internal/discord/client.go#L241)

- **Status (2026-11-09):** ✅ fixed. Discord/client.go fit() caps the embed at 6000 characters in total.
- **What:** Once a webhook has refused a components message (any HTTP 400 flips w.classic permanently), alerts go through BuildEmbed, which truncates each part individually (title 256, description 4096, 25 fields of 256/1024, footer 2048) but never enforces Discord's 6000-character limit across the whole embed. Discord rejects the oversize embed with 400, the alert falls to the fallback log, and the failure counts toward opening the breaker.
- **When it bites:** In classic mode, a log-cleanup or update alert with a ~3000-char description and a 1024-char field plus several others exceeds 6000 chars -> HTTP 400 -> alert not delivered; five such in a row open the breaker and block all Discord alerts for a minute.
- **Fix:** After per-field truncation, trim fields/description until the summed length of title, description, field names/values and footer is <= 6000.

#### 77. Firewall 'limit' rules on IPv6 count per /128, so one client with a /64 bypasses them

`gap-6` · security · verifier: **found by completeness critic (single reviewer, not independently re-verified)** · [backend/internal/firewall/build.go:155](backend/internal/firewall/build.go#L155)

- **Status (2026-11-09):** ✅ fixed. Firewall/build.go adds --hashlimit-srcmask 64 for IPv6.
- **What:** limitMatch emits `--hashlimit-mode srcip` without --hashlimit-srcmask. ip6tables therefore keeps one bucket per full /128 address, and Build writes the same limit rule into ip6tables when the host has IPv6. A residential or VPS IPv6 client controls at least a /64 (2^64 addresses), so it can rotate source addresses and never exceed the per-address rate. This is the firewall manager's own rule builder, separate from the DDoS defender (ddos-firewall-10).
- **When it bites:** The owner adds 'limit anyone on tcp port 22 to 6/min' to slow SSH brute force. An attacker with an IPv6 /64 picks a fresh source address for each connection. Every connection lands in a new hashlimit bucket, so the attacker gets unlimited attempts while the dashboard shows the limit as in force.
- **Fix:** For the IPv6 family, add `--hashlimit-srcmask 64` (or a configurable prefix) to limit rules. Also consider bounding --hashlimit-htable-size and --hashlimit-htable-max, so rotating addresses cannot grow the table without limit.

#### 78. Incident feedback and notices: unlocked read-modify-write loses updates, and a failed meta read wipes every entry

`gap-5` · data-safety · verifier: **found by completeness critic (single reviewer, not independently re-verified)** · [backend/internal/incidents/feedback.go:214](backend/internal/incidents/feedback.go#L214)

- **Status (2026-11-09):** ✅ fixed. Incidents/feedback.go: package mutex; keep aborts on a read error.
- **What:** keep() loads the whole incidents.feedback or incidents.notices JSON map with store.Meta, changes one entry, and writes the whole map back with SetMeta, without a lock across the two steps. store.Meta (storage/updates.go:61-70) ignores the Scan error and returns "" on any failure. load() then returns an empty map, and keep() writes back a map holding only the one entry being saved. Two concurrent saves, such as notices for two incidents from two tabs or a save racing a delete, also overwrite each other.
- **When it bites:** The owner publishes notices for incidents 41 and 42 from two browser tabs at nearly the same moment. Both requests load {}, and each writes back a map with only its own notice, so one published notice disappears from the public status page. Or a transient SQLite error during the read makes saving feedback on one incident erase all other feedback, which LearnedFrom uses to classify causes.
- **Fix:** Serialize keep() with a package-level mutex, or do the read and write inside one store transaction. Make Meta return (value, error), so a failed read aborts the write instead of being treated as empty.

#### 79. Kernel crash alerts from syslog files can be spoofed by any local user

`host-collect-13` · security · verifier: **confirmed** · [backend/internal/kernel/monitor.go:96](backend/internal/kernel/monitor.go#L96)

- **Status (2026-11-09):** ✅ fixed. Kernel/monitor.go kernelLogCrashes reads only kern.log.
- **What:** KernelLogCrash accepts any syslog line containing ' kernel: '. Any unprivileged process can write to /dev/log with the tag 'kernel' (`logger -t kernel 'Kernel panic - not syncing'`), and rsyslog writes the line into /var/log/syslog as 'host kernel: Kernel panic ...'. The journal check correctly restricts to _TRANSPORT=kernel; the file check does not.
- **When it bites:** A local user or a compromised service account runs `logger -t kernel "BUG: unable to handle page fault"`, and nightjar sends a Critical 'The kernel crashed' alert to the crash webhook. This can be repeated to spam alerts or to hide a real one inside the cooldown.
- **Fix:** Read only kern.log (facility kern, which userspace cannot forge through glibc), or rely on `journalctl _TRANSPORT=kernel` / dmesg, and drop /var/log/syslog and /var/log/messages from kernelLogCrashes.

#### 80. Journal reads keep only the oldest 2000 entries and advance lastJournal even on timeout, so newer kernel errors are permanently skipped

`host-collect-7` · correctness · verifier: **confirmed** · [backend/internal/kernel/monitor.go:352](backend/internal/kernel/monitor.go#L352)

- **Status (2026-11-09):** ✅ fixed. Kernel/monitor.go journal read is --reverse; lastJournal advances only after complete reads.
- **What:** journalctl prints oldest-first and the scanner stops at maxJournalEntries, so with more than 2000 matches the newest entries are dropped. CheckCrashes then sets m.lastJournal = time.Now() (line 736) even if the read was truncated or journalctl timed out after 15s and returned nothing. The next window starts at lastJournal-2min, so the skipped span is never read again. The early-warning pattern is very broad ('failed', 'error:', 'timeout', 'power', 'link down'...), so 2000 matches in an hour is reachable without tripping the 200 msg/s flood detector.
- **When it bites:** A NIC driver logs a 'link down'/'timeout' warning every second (3600/hour, below the flood rate). An Oops ('BUG: ...') late in that hour is past entry 2000 in the early-warning query, or the crash query times out on the busy journal. lastJournal still advances, so the event is never reported from the journal.
- **Fix:** Use `--reverse` (newest first) or `-n maxJournalEntries` so the cap drops the oldest entries. Advance lastJournal only when the query completed without a timeout or truncation, or advance it to the timestamp of the last entry processed.

#### 81. Stability score counts OOM kills since boot, so one old OOM inflates the risk until reboot

`host-collect-19` · correctness · verifier: **confirmed** · [backend/internal/kernel/monitor.go:532](backend/internal/kernel/monitor.go#L532)

- **Status (2026-11-09):** ✅ fixed. Kernel/monitor.go scores only the increase over LastOOMKills.
- **What:** oomKills reads the cumulative oom_kill counter from /proc/vmstat. CheckStability adds 25 points whenever it is above 0, with no delta from the previous check.
- **When it bites:** One OOM kill 20 days ago, after which memory briefly dips below 200MB (+15): the score is 40 and the crash webhook gets 'The kernel may crash' with 'Critical: OOM killer has killed 1 process(es)', for an event weeks old. This repeats each cooldown until reboot.
- **Fix:** Track the last oom_kill value in the persisted tracking state and score only the increase since the previous check.

#### 82. dmesg crash check filters to err,warn levels and misses Oops/BUG/panic messages, which the kernel logs at alert/emerg

`host-collect-14` · correctness · verifier: **plausible** · [backend/internal/kernel/monitor.go:637](backend/internal/kernel/monitor.go#L637)

- **Status (2026-11-09):** ✅ fixed. Kernel/monitor.go dmesg --level=emerg,alert,crit,err,warn.
- **What:** `dmesg --level=err,warn` excludes emerg, alert and crit. 'BUG: kernel NULL pointer dereference' and 'Unable to handle kernel paging request' are pr_alert, and 'Kernel panic' is pr_emerg, so the dmesg source never sees the lines its keyword list targets. When the journal checks are skipped (flood) or the journal is volatile, this redundancy is gone.
- **When it bites:** A non-fatal oops is logged at KERN_ALERT while the journal check is skipped because of a flood. dmesgCrashes filters it out, and if kern.log is absent (RHEL, or journald only) no crash is reported.
- **Fix:** Use `--level=emerg,alert,crit,err,warn`.

#### 83. Weekly maintenance windows shift by an hour on DST transition days

`monitor-core-11` · correctness · verifier: **confirmed** · [backend/internal/maintenance/maintenance.go:212](backend/internal/maintenance/maintenance.go#L212)

- **Status (2026-11-09):** ✅ fixed. Maintenance.go builds windows from the wall clock; Rename clones Checks.
- **What:** Window.At computes the start as local midnight plus `from` minutes of absolute duration (day.Add). On a DST change day, wall-clock time after the transition is off by an hour, so 04:00 becomes 05:00 (spring) or 03:00 (autumn). Checks are then held, or not held, during the wrong hour. Rename also writes into the shared Checks backing array (line 328), which copies returned by List/For still reference.
- **When it bites:** A weekly backup window 'Sun 04:00-04:30' in a US/EU timezone. On the spring-forward Sunday the window covers 05:00-05:30, so the 04:00 backup's downtime raises real alerts and counts against uptime.
- **Fix:** Build the start with time.Date(day.Year(), day.Month(), day.Day(), from/60, from%60, 0, 0, loc) and the end the same way (adding a day when To < From). Deep-copy Checks in List/For, or replace the slice in Rename instead of mutating it.

#### 84. Items in Failed status stay due while cleanup is off and are deleted with no new notice when it is turned back on

`integrations-7` · data-safety · verifier: **confirmed** · [backend/internal/mediacleanup/mediacleanup.go:310](backend/internal/mediacleanup/mediacleanup.go#L310)

- **Status (2026-11-09):** ✅ fixed. Mediacleanup.go resets Failed as well when cleanup is off.
- **What:** When MediaCleanup is turned off, Leaving and Waiting items are reset to Preview (LeavingAt=0), but Failed items keep their status and their old LeavingAt. sortedDue() includes Failed items with LeavingAt <= t. On the first pass after the cleanup is turned back on, they are removed at once, without being re-listed or announced with a fresh notice period.
- **When it bites:** A removal fails (for example, Plex media deletion is off), so the item is Failed. The owner turns the cleanup off. Months later they turn it on again, expecting the usual "Leaving on <date>" notice. Instead the Failed item, which someone may have started watching in the meantime but Nightjar did not see, is deleted in that very pass.
- **Fix:** Include Failed in the reset at line 310 (`it.Status == Leaving || it.Status == Waiting || it.Status == Failed`), so re-enabling starts a new notice period.

#### 85. Emergency 'cleanup freed nothing' Critical alert is ungated and repeats every 5 minutes

`monitor-core-8` · correctness · verifier: **confirmed** · [backend/internal/monitor/disk.go:124](backend/internal/monitor/disk.go#L124)

- **Status (2026-11-09):** ✅ fixed. Monitor/disk.go notify goes through the raiseAlertEvery gates.
- **What:** diskCleanup.emergency sends a Critical alert through ctx.send directly, bypassing the alert gate, every time it runs (every diskEmergencyCooldown=300s) while usage stays above the emergency threshold. The partition emergency/routine notifications (lines 282, 298) are likewise ungated.
- **When it bites:** Root sits at 96% with user data that cleanup cannot remove. 'The disk is 96% full, and cleanup freed nothing' (Critical) goes out every 5 minutes indefinitely, with no escalation and no ALERT_MAX_COOLDOWN cap.
- **Fix:** Route these notifications through h.raiseAlert / Gates with a per-mount key so the normal backoff applies.

#### 86. Duplicate early-warning alerts on NVMe partitions, and MONITOR_INDIVIDUAL_PARTITIONS=false does not switch to total-only alerts

`monitor-core-9` · correctness · verifier: **confirmed** · [backend/internal/monitor/disk.go:235](backend/internal/monitor/disk.go#L235)

- **Status (2026-11-09):** ✅ fixed. Monitor/disk.go: NVMe pass removed; alertTotal runs when partitions are off.
- **What:** For an NVMe partition between the early-warning and main thresholds, maintain() sends an early warning under key 'partition-early:<mnt>' (line 256), and the loop at lines 234-237 sends a second one under 'nvme:<mnt>'. Above DiskThreshold the NVMe early warning is still sent alongside the threshold alert. Separately, alertPartitions (line 177) ignores MonitorIndividualPartitions, and alertTotal returns whenever any partitions exist (line 202), so the setting described as 'Otherwise only the total across disks' never produces total alerts. thresholdAlert also shares the 'partition:<mnt>' gate key with alertPartitions, so it can never fire.
- **When it bites:** Root is on NVMe (most modern machines) and at 77% with defaults 75/80. Each cooldown period sends two identical 'early warning' Discord alerts. At 85% the owner gets both 'is 85% full' and an NVMe 'early warning'.
- **Fix:** Drop the separate NVMe pass, or skip it when maintain() already handled the partition. Make alertPartitions respect MonitorIndividualPartitions, and alertTotal fire when it is false. Remove or re-key the dead thresholdAlert.

#### 87. Aggressive log check never re-trims a log it already cleaned while the disk stays under pressure

`monitor-core-10` · correctness · verifier: **confirmed** · [backend/internal/monitor/logs.go:351](backend/internal/monitor/logs.go#L351)

- **Status (2026-11-09):** ✅ fixed. Monitor/logs.go re-trims a cleaned log once it grows past its post-clean size.
- **What:** In aggressive mode, candidates is every log file, so a cleaned path never drops out of `candidates` and stays in h.cleaned (lines 338-346). cleanOne returns early for any path in h.cleaned, so a log that regrows is never trimmed again until root falls below the aggressive threshold. That is the situation where trimming matters most.
- **When it bites:** Root is at 93% (aggressive threshold 92) because a service is spewing into /var/log/app.log. The log check truncates it once and marks it cleaned. The log refills the disk, and every later log check skips it while the disk heads to 100%.
- **Fix:** Clear the cleaned mark once the file's size grows past its post-clean size, or rely only on the lastAlert cooldown in aggressive mode.

#### 88. Hung network mount leaks one blocked goroutine (and OS thread) per cycle and silently disables disk monitoring

`monitor-core-3` · resource-leak · verifier: **plausible** · [backend/internal/monitor/resources.go:59](backend/internal/monitor/resources.go#L59)

- **Status (2026-11-09):** ✅ fixed. Collect/host.go per-mount timeout; resources.go CompareAndSwap busy flag.
- **What:** collect.Partitions calls disk.Usage (statfs) on every mount, NFS/CIFS included, and does so before the ShouldMonitor filter. It runs inside withTimeout, which on timeout returns the default and abandons the goroutine. A statfs on a hard-mounted, unreachable NFS share blocks uninterruptibly, so each cycle leaves one more goroutine stuck in a syscall, each pinning an OS thread. s.Partitions is then nil every cycle: DiskPercent is 0, and all disk alerts and cleanup stop with no warning. Separately, mediaCheck calls os.Stat(cfg.MediaDirectory) (line 321) and DirSize on the loop goroutine with no timeout guard for a hung mount.
- **When it bites:** The NAS behind /mnt/nas goes offline (hard mount). Every 5s cycle spawns a goroutine blocked in statfs. Disk usage reads as 0% from then on, so root filling up is never alerted. After about 10000 cycles (~14h) the Go runtime hits its 10000-thread limit and the process crashes fatally. If MEDIA_DIRECTORY is on that mount, the loop blocks forever in os.Stat.
- **Fix:** Run partition collection in a single long-lived worker and skip a new read while the previous one is still outstanding (singleflight, or a 'busy' flag like serviceWatcher.running). Stat each mount separately so one hung mount does not hide the others, and record mounts that time out. Apply the same guard to the media directory stat and size.

#### 89. Heartbeat secret token (and any credentials in check URLs) are posted in every Discord alert

`integrations-4` · security · verifier: **confirmed** · [backend/internal/monitor/services.go:136](backend/internal/monitor/services.go#L136)

- **Status (2026-11-09):** ✅ fixed. Monitor/services.go alertTarget leaves out heartbeat tokens and URL credentials.
- **What:** send() always adds a "Target" field holding spec.Target. For a heartbeat check the Target is the secret token, and that token is the only authentication on the unauthenticated /ping/{token} route (api/heartbeats.go). For HTTP checks the Target is the full URL, which often embeds basic-auth credentials or ?apikey= query tokens. These alerts go to Discord or other channels and into stored notification history, where they are read by people who are not dashboard admins.
- **When it bites:** A nightly backup heartbeat goes down and the alert shows Target `Xy3...token`. Anyone in the channel, or with access to a forwarded or relayed copy, can curl https://host/ping/<token> after each failed run. That marks the job as succeeded and hides a broken backup, or they can hit /fail to raise false alerts. An HTTP check on https://user:pass@nas.local/ leaks NAS credentials the same way.
- **Fix:** Leave out the Target field for heartbeat checks, or show a redacted form. For URLs, strip userinfo and redact sensitive query parameters before putting them in alerts.

#### 90. Telegram message truncated on the HTML string can cut tags/entities, making Telegram reject the alert

`notify-incidents-5` · correctness · verifier: **confirmed** · [backend/internal/notify/channels.go:77](backend/internal/notify/channels.go#L77)

- **Status (2026-11-09):** ✅ fixed. Notify/channels.go truncates text before escaping HTML.
- **What:** The Telegram text is built as HTML (<b>, <code>, <i> footer, &amp; entities) and then discord.Truncate(text, 4096) cuts it by runes. Any alert whose rendered HTML exceeds 4096 runes is cut at an arbitrary point, which can leave an unclosed <b>/<code>/<i> or a partial entity; with parse_mode HTML Telegram answers 400 'can't parse entities' and the alert is not delivered.
- **When it bites:** A long update/kernel/log-cleanup alert (description plus several 1024-char fields) renders to ~5000 runes; truncation lands inside `<b>Logs:</b>` or after `<i>` of the footer -> Telegram returns HTTP 400 and the alert never reaches Telegram (counts toward opening its breaker).
- **Fix:** Truncate the plain parts (description, field values) before HTML-escaping/formatting, or truncate on a tag boundary and drop the footer, keeping the final string well-formed.

#### 91. Slack payload does not escape <, >, & so untrusted alert text can trigger @channel mentions or spoofed links

`notify-incidents-6` · security · verifier: **confirmed** · [backend/internal/notify/channels.go:99](backend/internal/notify/channels.go#L99)

- **Status (2026-11-09):** ✅ fixed. Notify/channels.go escapes &, <, > before converting bold.
- **What:** Slack parses `<!channel>`, `<!everyone>`, `<@U..>` and `<url|label>` in message text and attachment text; Slack requires &, <, > to be escaped. NewSlack sends a.Title/a.Description/field values raw. Alert text includes externally controlled strings such as torrent names (DownloadComplete/StalledTorrent titles), container names and kernel log lines. Discord sends use allowed_mentions parse [] and Telegram escapes HTML, but Slack has no equivalent.
- **When it bites:** A torrent from a public indexer named `<!channel> <https://evil.example|Plex login required>` finishes; the 'Downloaded: ...' alert pings everyone in the Slack channel and shows a disguised phishing link.
- **Fix:** Escape &, <, > (to &amp; &lt; &gt;) in all text sent to Slack before applying the bold conversion.

#### 92. Email HTML part is emitted as one line far over the 998-octet SMTP limit

`notify-incidents-7` · correctness · verifier: **confirmed** · [backend/internal/notify/channels.go:195](backend/internal/notify/channels.go#L195)

- **Status (2026-11-09):** ✅ fixed. Notify/channels.go both MIME parts are quoted-printable.
- **What:** The whole HTML body (inline-styled divs, h2, table rows for every field, footer and link) is written as a single line with no Content-Transfer-Encoding (quoted-printable/base64). The fixed template alone is ~650 characters and each field row adds ~140+ characters plus its value, so most alerts exceed RFC 5321/5322's 998-octet line limit; long descriptions in the text/plain part can too.
- **When it bites:** An alert with 3+ fields is sent through a strict relay: it is rejected ('line too long') so the email channel fails, or the MTA hard-wraps the line by inserting CRLF mid-attribute/mid-tag, corrupting the rendered email.
- **Fix:** Encode both parts with Content-Transfer-Encoding: quoted-printable (mime/quotedprintable) or base64, which keeps lines under 76 characters.

#### 93. Plugin notify channel has no circuit breaker; a hung plugin stalls the synchronous monitor loop up to 2 minutes per alert

`notify-incidents-4` · resource-leak · verifier: **plausible** · [backend/internal/notify/channels.go:306](backend/internal/notify/channels.go#L306)

- **Status (2026-11-09):** ✅ fixed. Notify/channels.go plugin sends go through a circuit breaker, capped at 20s.
- **What:** Every HTTP channel and email wraps delivery in a CircuitBreaker, but NewPlugin does not; main.go gives each plugin send a 2-minute context. Hub.deliver waits (wg.Wait) for all targets, and alerts are sent synchronously from Loop.Evaluate (raiseAlert -> send), as is the quiet-hours summary in Tick. A plugin that hangs or is slow therefore blocks the whole monitoring cycle for up to 2 minutes on every alert, every time, with no back-off.
- **When it bites:** A notify plugin's endpoint black-holes connections. Three checks go down in one cycle -> three alerts, each waiting 2 min for the plugin -> the monitor loop is stuck ~6 min: no CPU/disk/temperature evaluation, Watchdog heartbeat goes stale (spurious 'monitor stuck' alerts), and every subsequent alert keeps paying the full timeout because nothing opens a breaker.
- **Fix:** Wrap plugin sends in a CircuitBreaker like the other channels and use a much shorter timeout; consider not waiting on slow channels in deliver (record outcome asynchronously) so one channel cannot block the loop.

#### 94. 8 MB response cap makes media cleanup fail on large Plex libraries

`integrations-6` · correctness · verifier: **confirmed** · [backend/internal/plex/client.go:81](backend/internal/plex/client.go#L81)

- **Status (2026-11-09):** ✅ fixed. Plex/client.go detects truncation (reads limit+1); items paged 500 at a time.
- **What:** get() reads at most 8 MB and then json.Unmarshal runs on the truncated bytes. Items() fetches a whole library section's /all with includeGuids, and each movie carries Media/Part, guids, genres, roles and so on, which is several KB per item in JSON. A library of a few thousand movies goes over 8 MB, Unmarshal fails with "unexpected end of JSON input", and the error comes back as "unexpected response". Truncation should be reported, not handed to the parser.
- **When it bites:** A 4,000-movie library gives a 10+ MB response. Every cleanup pass then fails with "could not list Movies: unexpected response: unexpected end of JSON input", so the feature never works for that library, and the message doesn't say the response was too large.
- **Fix:** Stream-decode with json.NewDecoder and a larger bound, or page /all with X-Plex-Container-Start/Size. Detect truncation (read limit+1 bytes) and return a clear error.

#### 95. Plugin ownership and permission checks run only at Scan, skip intermediate directories, and are not repeated before the plugin runs as root

`storage-config-main-7` · security · verifier: **confirmed** · [backend/internal/plugins/plugins.go:252](backend/internal/plugins/plugins.go#L252)

- **Status (2026-11-09):** ✅ fixed. Plugins.go runnable() now checks every folder above the real plugin folder up to / (root-owned sticky folders such as /tmp excepted), before every run; test/plugins TestPluginUnderWritableAncestorIsRefused.
- **What:** problem() checks owner and go-w on just three paths: p.Dir, plugin.json and the resolved program. It does not check directories between p.Dir and the program (e.g. `run: "bin/check.sh"` with bin/ group- or world-writable). It does not check the parents of an EvalSymlinks-resolved plugin folder (a plugins/foo symlink into /home/alice/foo). It does not check the plugins folder itself; selfcheck.Exposed walks only up from PLUGINS_DIR, not into it. The result is cached in p.Problem. run() (line 490) only re-resolves the program path and checks that it stays inside the folder. It never re-checks ownership or mode, so anything changed after the last scan runs as root.
- **When it bites:** A plugin has `run: "bin/run.sh"` and bin/ is mode 0777, or the plugin folder is a symlink into a user's home directory. At scan time run.sh is root-owned 0755, so the plugin passes and the owner enables it. The local user then replaces bin/run.sh (or renames the folder in their home and recreates it) with their own script. No rescan happens, and the next check, card refresh or alert runs their script as root with the plugin's secret settings in its environment.
- **Fix:** Run the ownership and permission check on every path component from the resolved plugin folder down to the program (and up to the root for the resolved folder), as sshd's StrictModes does. Repeat the check inside run() right before exec, and refuse to run if it fails.

#### 96. Inactive-torrent auto-remove deletes torrents the dashboard just added in the stopped state, and paused partial downloads

`integrations-9` · correctness · verifier: **confirmed** · [backend/internal/qbittorrent/client.go:81](backend/internal/qbittorrent/client.go#L81)

- **Status (2026-11-09):** ✅ fixed. Qbittorrent/client.go inactive removal requires Progress >= 1.
- **What:** RemoveReason returns "inactive" for any state in InactiveStates, which includes pauseddl and stoppeddl (incomplete downloads). The dashboard's Add endpoint lets the user add torrents stopped (Add(..., stopped=true)). With INACTIVE_TORRENT_AUTO_REMOVE on, the torrent watcher removes them within one or two loop cycles. Partial files stay on disk, and the torrent and its progress are gone.
- **When it bites:** With auto-remove of paused torrents enabled, the user adds a magnet with "Add stopped" so they can pick files first. Seconds later the next torrent cycle sees state stoppeddl, calls Delete(hash, false), and the torrent disappears from qBittorrent. Its partial data is left orphaned on disk.
- **Fix:** Limit "inactive" removal to completed torrents (pausedup/stoppedup, Progress>=1), or require the torrent to have been inactive for a configurable time before removing it.

#### 97. qBittorrent client builds an invalid URL for IPv6 hosts without a port

`integrations-10` · correctness · verifier: **confirmed** · [backend/internal/qbittorrent/client.go:120](backend/internal/qbittorrent/client.go#L120)

- **Status (2026-11-09):** ✅ fixed. Qbittorrent/client.go uses net.JoinHostPort.
- **What:** When the host has no port, New sets u.Host = fmt.Sprintf("%s:%d", u.Hostname(), port). Hostname() strips the IPv6 brackets, so the result is invalid, e.g. "::1:8080".
- **When it bites:** QBITTORRENT_HOST=http://[::1] with port 8080 gives the base http://::1:8080. Every request then fails to parse or dial, and qBittorrent monitoring stays disconnected with a confusing error.
- **Fix:** Use u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(port)).

#### 98. Pinned HTTPS clients for failed Add/Update tokens are cached forever

`remote-agent-6` · resource-leak · verifier: **confirmed** · [backend/internal/remote/remote.go:598](backend/internal/remote/remote.go#L598)

- **Status (2026-11-09):** ✅ fixed. Remote/remote.go forgetUnusedClient on a failed Add/Update.
- **What:** client() caches a new http.Client and Transport in m.clients[token] for every token it is asked about. Add (line 352) and Update (line 406) call fetch with a token typed by the user before saving anything. If the fetch fails (wrong token, wrong port, typo), the host is never saved, so forgetClient is never called for that token, and the map only grows for the process lifetime.
- **When it bites:** An admin repeatedly tries to add servers with mistyped or regenerated tokens. Each attempt leaves a Transport with its own TLS config and connection pool in m.clients. This is unbounded, if slow, growth in a long-running root daemon.
- **Fix:** For Add/Update verification, use a throwaway client and close its idle connections afterwards, or call m.forgetClient(token) when the verification fetch fails or the host is not saved.

#### 99. Authorize-with-password trusts an unseen host key and sends the account password on first contact

`remote-agent-7` · security · verifier: **confirmed** · [backend/internal/remote/ssh.go:574](backend/internal/remote/ssh.go#L574)

- **Status (2026-11-09):** ✅ fixed. Remote/ssh.go refuses unconfirmed host keys; StrictHostKeyChecking=yes.
- **What:** AuthorizeKey logs in with password auth using sshArgs, which set StrictHostKeyChecking=accept-new. For a server Nightjar has never contacted, ssh accepts whatever host key is presented and then the askpass helper hands over the password (often root's). The user never sees or confirms the fingerprint before the password is sent; the host key is only shown afterwards in SSHCheck.HostKey.
- **When it bites:** An attacker on the path between the dashboard and a new server, or one controlling DNS for the SSH host name the admin entered, answers SSH on the first connection. Nightjar accepts the attacker's host key, and the attacker receives the server's root password through password/keyboard-interactive auth.
- **Fix:** For the password flow, first fetch the host key (ssh-keyscan) and show its fingerprint for the user to confirm, then pass it as a pinned known_hosts entry with StrictHostKeyChecking=yes. Alternatively, require a key-based test (which has already accepted the key) before allowing a password login.

#### 100. Weekly report is marked sent before delivery, so a transient failure loses that week's report

`notify-incidents-10` · error-handling · verifier: **confirmed** · [backend/internal/report/report.go:366](backend/internal/report/report.go#L366)

- **Status (2026-11-09):** ✅ fixed. Report/report.go writes sentKey only after a successful send.
- **What:** Schedule writes sentKey = now before calling Send. If Send returns false (Discord/channels unreachable at that minute, or c.Webhook route reports failure), the next tick sees sent >= due and skips; the report is never retried for that week.
- **When it bites:** At Monday 09:00 the internet connection is briefly down; Send fails on every channel; sentKey already holds 09:00, so the weekly report is silently skipped until next Monday.
- **Fix:** Only persist sentKey after a successful Send (or record attempts and retry for a bounded period within the 24-hour window).

#### 101. A heartbeat that has never been pinged loses its listening start on every restart and may never go late

`integrations-8` · correctness · verifier: **confirmed** · [backend/internal/services/heartbeat.go:222](backend/internal/services/heartbeat.go#L222)

- **Status (2026-11-09):** ✅ fixed. Services/heartbeat.go saves Seen at once when nothing is stored.
- **What:** pings() creates a record with Seen=now and keeps it only in memory. It is written to the store only when a ping arrives (Ping -> SetMeta). judge() treats both missed-run checks relative to Seen: `ref := max(p.OK, p.Seen)` for "every" schedules, and `prevT < p.Seen` meaning "no run was due" for cron. For a job that has never pinged, each Nightjar restart resets Seen to the restart time. Nightjar restarts itself to apply settings and on updates.
- **When it bites:** A new "every 1d" heartbeat is created for a backup whose cron entry was mistyped, so it never pings. If Nightjar restarts at least once a day (a settings change, an auto-update, a reboot), now is never past Seen+1d+grace. The check shows "no ping yet; next due by …" indefinitely and never alerts.
- **Fix:** Save the record, Seen included, when it is first created in pings(), or set Seen from the check's creation time stored with the spec.

#### 102. Drives behind RAID controllers (megaraid/cciss) share one map key and overwrite each other

`host-collect-17` · correctness · verifier: **confirmed** · [backend/internal/smart/smart.go:114](backend/internal/smart/smart.go#L114)

- **Status (2026-11-09):** ✅ fixed. Smart/smart.go keys drives by device and type.
- **What:** smartctl --scan reports drives behind a hardware RAID controller with the same Name and different types ("/dev/bus/0" with megaraid,0 / megaraid,1 ...). Reader keys r.drives by d.Device only, so later drives overwrite earlier ones and r.order holds duplicates. readOne also looks up `prev` by device alone.
- **When it bites:** On a server with 8 disks behind a MegaRAID controller, the dashboard (Drives()) shows 8 copies of the last disk. When one disk is asleep or errors, readOne returns another disk's previous reading under its name.
- **Fix:** Key by Device+"|"+Type (or by serial once known) in drives, order and the prev lookup.

#### 103. repairs table is never pruned (PruneRepairs has no caller)

`storage-config-main-9` · resource-leak · verifier: **confirmed** · [backend/internal/storage/repairs.go:81](backend/internal/storage/repairs.go#L81)

- **Status (2026-11-09):** ✅ fixed. Storage/store.go Prune calls PruneRepairs.
- **What:** Every other history table is pruned from the loop: checks, samples, notifications, activity and plex_plays. Store.PruneRepairs exists but nothing calls it. Each repair row stores the script's output (up to repair.maxOutput) plus detail, and it is kept forever regardless of HISTORY_RETENTION_DAYS.
- **When it bites:** A flapping check with a script repair runs one attempt per outage. Over months the repairs table, holding kilobytes of output per row, grows without bound in nightjar.db. Repairs("", limit) scans an ever-growing table, and the history retention the settings promise does not apply to it.
- **Fix:** Call ctx.Store.PruneRepairs(s.Now - HistoryRetentionDays*86400) next to the checks prune in monitor/services.go.

#### 104. Analytics 'points' is null when the database is unavailable, so the Analytics page and server detail chart crash

`x-contract-5` (also reported as fe-pages-views-1) · frontend-state · verifier: **confirmed** · [backend/internal/storage/samples.go:53](backend/internal/storage/samples.go#L53)

- **Status (2026-11-09):** ✅ fixed. Storage/samples.go SampleBuckets returns an empty slice on every early exit.
- **What:** SampleBuckets returns nil when width <= 0, when the DB is unavailable (db == nil) or when the query fails. analytics.Read stores that in History.Points (`json:"points"`, no omitempty), so the API sends "points": null. The TS type is AnalyticsPoint[] (non-null), and the UI calls h.points.reduce/.length/.some (pages/Analytics.tsx:73, 150, 206) and data.host.points.map (pages/Servers.tsx:97). The response even carries kept:false, and Analytics.tsx:289 has a banner for exactly this database-unavailable state, but Tiles (line 293) throws before it can be seen.
- **When it bites:** The SQLite file becomes unwritable or locked and Store reports unavailable, or a samples query errors. Opening Servers -> Analytics then throws 'Cannot read properties of null (reading reduce)' in Tiles and the page shows the error boundary instead of 'The database is unavailable'. A server's detail chart for 24h/7d/30d crashes the same way.
- **Fix:** Return []Sample{} from SampleBuckets on every early exit (or normalise in analytics.Read), and guard with (h.points ?? []) in the UI.

#### 105. Poll/Refresh race produces a spurious 'service failed' alert right after a dashboard restart

`updates-apps-10` · concurrency · verifier: **confirmed** · [backend/internal/units/units.go:101](backend/internal/units/units.go#L101)

- **Status (2026-11-09):** ✅ fixed. Units/units.go Refresh bumps counters; Poll skips units refreshed after its read began.
- **What:** Poll calls m.read() (systemctl list-units/show) without holding m.mu, then takes the lock and compares with m.units. If a dashboard restart's Refresh stores the unit as active in between, Poll compares its stale 'failed' reading with the fresh 'active' prev and emits EventFailed. The next poll then emits EventRecovered.
- **When it bites:** A unit is failed. The background poll reads it as failed. The admin clicks Restart, and Control and Refresh finish and set m.units[name] to active. Poll then takes the lock and sends '<unit> failed ... Restart it from the dashboard' to Discord, followed by 'running again' on the next poll.
- **Fix:** Keep a generation counter or per-unit refresh timestamp, and skip transitions for units refreshed after the poll's read started.

#### 106. Root update commands lose $TAG/$VERSION/DEBIAN_FRONTEND when run through sudo (env_reset)

`updates-apps-8` · correctness · verifier: **confirmed** · [backend/internal/updates/exec.go:85](backend/internal/updates/exec.go#L85)

- **Status (2026-11-09):** ✅ fixed. Updates/exec.go sudo wrapper passes the env through.
- **What:** When the monitor is not root, Root commands become `sudo -n -- argv` while the extra environment (c.Env) is set on the sudo process. sudo's default env_reset drops it, so DEBIAN_FRONTEND=noninteractive, and the VERSION/TAG values that release UpdateCommands are documented to see, never reach the command.
- **When it bites:** On a non-root monitor with passwordless sudo, the Seerr recipe runs `git fetch --force origin "+refs/tags/$TAG:refs/tags/$TAG"` with TAG empty and fails. Any custom update command using $VERSION downloads the wrong URL. apt-get runs without the noninteractive debconf frontend.
- **Fix:** Pass the variables through sudo explicitly, e.g. argv = ["sudo","-n","--","env", c.Env..., argv...], or use `sudo --preserve-env=VERSION,TAG,DEBIAN_FRONTEND` where policy allows.

#### 107. *arr targets read from config.xml always use 127.0.0.1, ignoring a configured BindAddress

`updates-apps-9` · correctness · verifier: **confirmed** · [backend/internal/updates/kinds.go:311](backend/internal/updates/kinds.go#L311)

- **Status (2026-11-09):** ✅ fixed. Updates/kinds.go uses BindAddress with JoinHostPort.
- **What:** arrConfig parses BindAddress, but arrEndpoint always builds http://127.0.0.1:<port>. Sonarr, Radarr and Prowlarr bound to a specific interface address do not listen on loopback.
- **When it bites:** Radarr's config.xml has <BindAddress>192.168.1.10</BindAddress>. Detection adds the target, but every check fails with 'connection refused', so updates are never offered or installed.
- **Fix:** Use BindAddress when it is a concrete IP (not '*' or empty), bracketing IPv6, and otherwise use 127.0.0.1.

#### 108. Git targets silently run as root (with safe.directory=*) when the checkout owner can't be resolved

`updates-apps-6` · security · verifier: **confirmed** · [backend/internal/updates/kinds.go:470](backend/internal/updates/kinds.go#L470)

- **Status (2026-11-09):** ✅ fixed. Updates/kinds.go owner() errors for a uid with no account.
- **What:** gitUser() returns owner(s.Dir), and owner() returns "" whenever os.Stat fails or the uid has no passwd entry (user.LookupId fails). An empty User means Exec runs the command as the monitor (root). git is invoked with `-c safe.directory=*`, which turns off git's protection against repositories owned by other users. The UpdateCommand also runs as root. The intended privilege drop is skipped silently in exactly the case where the checkout belongs to an unknown account.
- **When it bites:** A git target's directory is owned by uid 1000 with no /etc/passwd entry (common for directories created by a container or copied from another machine). owner() returns "", so `git fetch`/`git pull` and the build command run as root. Whoever controls uid 1000 can set core.sshCommand, credential.helper='!...' or hooks in .git and get root code execution on the next check or apply.
- **Fix:** When the owner is not root and cannot be resolved to a name, fail the check with a clear error (or run as the numeric uid via `setpriv --reuid`/`runuser` with the uid) instead of falling back to root. Drop `safe.directory=*` when running as root on a directory root does not own.

#### 109. Check round is queued twice per interval because Tick ignores an in-progress round

`updates-apps-7` · correctness · verifier: **confirmed** · [backend/internal/updates/manager.go:380](backend/internal/updates/manager.go#L380)

- **Status (2026-11-09):** ✅ fixed. Updates/manager.go sets checking on dequeue; Tick skips while checking.
- **What:** work() deletes queued["check"] when it dequeues the job, and checkAll() updates lastCheck only when the whole round ends. A round can take minutes (apt-get update with a 10-minute timeout, git fetches, GitHub calls). During that time every Tick still sees now-last >= interval and enqueues another "check". As soon as the round ends a second full round starts. Auto-applies are also held back because Tick returns early while checking or queued["check"] is set.
- **When it bites:** With the default 6h interval and a round lasting 2 minutes, every interval runs two full rounds back to back: apt-get update twice, and each release target hits the GitHub API twice, which doubles use of the unauthenticated 60/hour limit. Automatic updates in the window start only after both rounds finish.
- **Fix:** In Tick, skip enqueueing the round when m.checking is true (or set lastCheck when the round starts).

#### 110. env.recommended documents DISK_FULL_THRESHOLD, a safeguard that does not exist

`storage-config-main-10` · maintainability · verifier: **confirmed** · [env.recommended:329](env.recommended#L329)

- **Status (2026-11-09):** ✅ fixed. DISK_FULL_THRESHOLD removed from env.recommended.
- **What:** The recommended .env says the monitor stops writing its own log file once disk usage passes DISK_FULL_THRESHOLD and resumes 5 points below it. No Go code reads DISK_FULL_THRESHOLD: it is not a config field and not read with os.Getenv. logx.RotatingFile always writes.
- **When it bites:** An operator relies on the documented behaviour, perhaps raising LOG_MAX_BYTES or LOG_BACKUP_COUNT, believing Nightjar will stop logging when the disk is nearly full. On a full disk Nightjar keeps writing nightjar.log (up to MaxBytes × (Backups+1)) and competes for the last free space.
- **Fix:** Either remove the variable from env.recommended or implement it in logx (skip file writes while the filesystem holding LOG_FILE is above the threshold).

#### 111. A failed /api/config at startup is never retried, leaving the shell without its config

`fe-core-4` · error-handling · verifier: **plausible** · [frontend/src/App.tsx:334](frontend/src/App.tsx#L334)

- **Status (2026-11-09):** ✅ fixed. App.tsx retries the config fetch with backoff; boot screen waits for it.
- **What:** Shell fetches the config once per authKey/configKey and swallows errors with `.catch(() => undefined)`. If that single request fails (for example the monitor is restarting while an open tab reloads onto a new build via freshness.ts or waitForRestart's last-resort reload), `config` stays undefined for the whole session. Then the Media tab is filtered out (plex_enabled/qbittorrent_enabled undefined), hidden sections and needs_setup are ignored, the saved appearance and custom CSS are not applied, and on a fresh install the gate shows SignIn instead of CreateAdmin because `config?.account` is undefined.
- **When it bites:** Nightjar restarts after a settings change. waitForRestart gives up after about 60s and reloads while the service is still coming up, so GET /api/config fails. Once the backend is back, the services poll recovers but the nav still has no Media tab and the dashboard look is the default, until the user reloads by hand.
- **Fix:** Retry the config fetch with backoff on failure (or poll it at a slow interval until it succeeds), and treat 'config not loaded' as its own loading state.

#### 112. Activity log hides or duplicates entries after 'Older' once new activity arrives

`fe-pages-views-5` · frontend-state · verifier: **confirmed** · [frontend/src/components/ActivityPanel.tsx:197](frontend/src/components/ActivityPanel.tsx#L197)

- **Status (2026-11-09):** ✅ fixed. ActivityPanel.tsx merges entries and dedupes by id.
- **What:** The list is `[...polledNewest100, ...older]`. The newest page is re-polled every 30 s, but `older` was fetched with `before=<last id of the first page as it was then>`. When new rows are added, the polled page shifts forward, and the rows that used to be at its tail fall into a gap between the two lists. When rows are pruned (kept a year), the page shifts the other way, and the same ids show up in both lists, giving duplicate React keys.
- **When it bites:** The activity log holds ids 1..200. The page shows 200..101, and the user clicks Older, so older = 100..1. Five changes happen (e.g. 'Turned off two-factor sign-in', 'Restored a backup'), and the next poll returns 205..106. The list now shows 205..106 followed by 100..1, so entries 105..101 silently disappear from the audit view until a reload.
- **Fix:** Once older pages are loaded, stop replacing the head and instead merge new entries by id (prepend ids > current max), or dedupe and merge by id with a Map. Alternatively re-fetch `older` from the current tail.

#### 113. Firewall form sends a stale protocol after the port is cleared, and the protocol control is disabled

`fe-pages-views-9` · correctness · verifier: **confirmed** · [frontend/src/components/Firewall.tsx:230](frontend/src/components/Firewall.tsx#L230)

- **Status (2026-11-09):** ✅ fixed. Firewall.tsx sends proto only with a port.
- **What:** `proto: draft.proto || undefined` is sent whatever the port is, but the protocol <select> is `disabled={!draft.port.trim()}`. After a rule is started from an open port (port 22, proto tcp) and the port is then cleared, proto stays 'tcp' and can't be changed. The backend rejects the rule with 'give a port for tcp or udp alone, or leave both' (firewall.go:704).
- **When it bites:** The user clicks 'Add rule' on 22/tcp under Open ports, clears the Port field, and types 203.0.113.7 to block that address on every port. Submit fails with an error about the protocol, while the Over control is greyed out showing 'tcp'. The user can't block the address without re-typing a port, switching the protocol, then clearing the port again.
- **Fix:** Send `proto: draft.port.trim() ? draft.proto || undefined : undefined`, or reset draft.proto to '' when the port is emptied.

#### 114. Firewall manager crashes when status.listening is null (not normalised server-side)

`fe-pages-views-2` · api-contract · verifier: **plausible** · [frontend/src/components/Firewall.tsx:285](frontend/src/components/Firewall.tsx#L285)

- **Status (2026-11-09):** ✅ fixed. Listening normalised in the backend; withLists applied in every setSt.
- **What:** FirewallManager does `st.listening.filter(...)` on every render (line 285), and also `st.listening.length` (around line 473) and `st.listening.filter` in presets(). The TS type marks `listening` as required, but the backend can send null. firewall.Listening() builds `var out []Port` and returns nil when nothing is parsed from /proc/net/{tcp,udp}*. remote.firewallCall (backend/internal/remote/firewall.go:119-127) normalises Rules, Drops and Open to empty slices but not Listening. That gap matters most for the older (non-`full`) agents the UI explicitly supports ('This agent runs an older version...'): their answer may not include `listening` at all, which Go decodes to nil and re-encodes as null. With no error boundary, the throw blanks the whole SPA.
- **When it bites:** The user picks an agent whose firewall answer has no `listening` field (an agent from before the field existed), or whose /proc/net reads return nothing (restricted /proc, hidepid, minimal container). GET /api/hosts/{id}/firewall returns `"listening": null`, FirewallManager throws `Cannot read properties of null (reading 'filter')`, and the Network > Firewall page and the rest of the dashboard go blank.
- **Fix:** In remote.firewallCall add `if v.Firewall.Listening == nil { v.Firewall.Listening = []firewall.Port{} }`, and in Manager.Status initialise `Listening` to an empty slice. On the frontend, use `const listening = st.listening ?? []` and make the type optional, matching open/drops.

#### 115. Select shows the first option as chosen when value is not among the options

`fe-pages-views-4` · correctness · verifier: **confirmed** · [frontend/src/components/Select.tsx:25](frontend/src/components/Select.tsx#L25)

- **Status (2026-11-09):** ✅ fixed. Select.tsx no clamp to 0; aria-selected only on a real match.
- **What:** `selected = Math.max(0, options.findIndex(...))` falls back to index 0. So the trigger renders `options[0].label` (the `?? value` fallback can't fire while options is non-empty), and option 0 is marked aria-selected with the check mark. The control then claims a choice the parent never made.
- **When it bites:** 1) Network page: the user clicks the card of a down virtual interface (all interfaces get cards, but `choices` filters to `!virtual || up`). The page charts that interface, but the Interface Select says the first physical interface (e.g. eth0) is selected. 2) Network > Firewall with a stale `?server=<removed-id>` link: the page says 'No server with an agent by that name' while the Machine Select shows 'This machine' as chosen.
- **Fix:** Use `selected = options.findIndex(...)` (may be -1). Render `value` (or a placeholder) when it is -1, and set aria-selected only on a real match. In Network, build the options from all interfaces, or include the picked one.

#### 116. Select crashes on Enter when its options shrink while open (or when empty)

`fe-pages-views-3` · correctness · verifier: **confirmed** · [frontend/src/components/Select.tsx:60](frontend/src/components/Select.tsx#L60)

- **Status (2026-11-09):** ✅ fixed. Select.tsx bounds-checked pick; clamps active; won't open with no options.
- **What:** `pick(i)` does `onChange(options[i].value)` without checking bounds. `active` is set when the menu opens and by arrow keys, but it isn't clamped when the `options` prop changes while the menu is open. With an empty options list, ArrowDown or Enter sets active=0 (`Math.max(0, Math.min(-1, i))`), and a second Enter calls pick(0) on undefined. Finder handles this case with `if (!i) return`; Select doesn't. With no error boundary the TypeError blanks the app.
- **When it bites:** On the Network page the Interface Select's options are rebuilt every second from the 1 s poll (`choices`). The user opens it and presses End (active = last index, e.g. 3). A Docker container stops, its veth interface disappears, and choices shrinks to 3 items. The user presses Enter, pick(3) reads `options[3].value` on undefined, and the dashboard goes blank.
- **Fix:** In pick(), do `const o = options[i]; if (!o) return;`. Also clamp `active` to `options.length - 1` in an effect when options change, and don't open the menu when there are no options.

#### 117. TimePicker panel can't be closed with Escape while focus is on the field

`fe-pages-views-10` · accessibility · verifier: **confirmed** · [frontend/src/components/TimePicker.tsx:258](frontend/src/components/TimePicker.tsx#L258)

- **Status (2026-11-09):** ✅ fixed. TimePicker.tsx closes on Escape and on blur.
- **What:** Opening the panel leaves focus on the trigger button (nothing moves focus into the dialog), but Escape is handled only by the panel's onKeyDown (onPanelKey). The trigger's onKey only handles ArrowUp/ArrowDown. A keyboard user who opens the picker with Enter or Space can't dismiss it with Escape. Tab also moves past the trigger into the panel's buttons and leaves it open, and there is no outside-focus close. The dialog role with aria-haspopup='dialog' and no focus management does not follow the dialog pattern.
- **When it bites:** In Notification rules > Quiet hours, a keyboard user focuses 'Quiet hours start' and presses Enter, and the panel opens. Escape does nothing because the event goes to the trigger and onKey ignores it. The panel stays open over the page until the user tabs through 24+12 buttons to 'Done' or clicks with a mouse.
- **Fix:** Handle Escape in the trigger's onKey too (`if (open && e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); setOpen(false); }`). Move focus to the selected hour on open, and close when focus leaves `root`.

#### 118. scopeCSS can be escaped with an unbalanced '}' or hoisted at-rules, so preview CSS styles the whole dashboard

`fe-core-6` · security · verifier: **confirmed** · [frontend/src/customCode.ts:89](frontend/src/customCode.ts#L89)

- **Status (2026-11-09):** ✅ fixed. CustomCode.ts drops a stray '}' and @import/@property, prefixes keyframes (@counter-style still hoisted, minor).
- **What:** The tokenizer clamps depth at 0 on a stray '}' and keeps that '}' as its own rule. The output `@scope (root) {\n}\n.masthead{...}\n}` therefore closes the @scope block early, and every later rule applies to the entire dashboard document. Hoisted at-rules (@import, @font-face, @keyframes, @property) are also emitted globally (line 103), so a status-page @font-face named 'IBM Plex Sans' or an @import of remote CSS changes the admin dashboard. The module comment promises the CSS 'never touches the dashboard'. Only the admin can author this CSS, so the impact is limited to breaking or restyling the designer page, for example after pasting a third-party CSS snippet.
- **When it bites:** The status-page CSS contains `} .masthead, nav { display:none }`, or a malformed snippet with one extra '}'. Opening Settings -> Status page designer hides the dashboard's navigation and masthead (or restyles the whole page) for as long as the preview is mounted.
- **Fix:** Reject or drop unbalanced closing braces while tokenizing. Render the preview inside an iframe (srcdoc with sandbox) or a shadow root instead of relying on @scope, which also contains hoisted @font-face, @import and @keyframes.

#### 119. Auto-reload onto a new build discards unsaved non-text edits and aborts in-flight actions

`fe-core-5` · data-safety · verifier: **confirmed** · [frontend/src/freshness.ts:26](frontend/src/freshness.ts#L26)

- **Status (2026-11-09):** ✅ fixed. Freshness.ts safe() checks typed input, drafts and pending writes.
- **What:** useNewBuild reloads a hidden tab (line 47), or a tab when it becomes visible again, whenever `typed` is false. `typed` is set only by `input` events from text inputs, textareas and selects. It ignores checkboxes and radios on purpose, and it never sees changes made through buttons (Seg toggles in AppearancePanel, the custom Select/TimePicker), drag-to-reorder of firewall rules, or a mutation request that is still in flight. A reload aborts any pending fetch, which also cancels the server's r.Context()-bound work (see fe-core-2).
- **When it bites:** The admin toggles several unsaved checkbox settings, or reorders firewall rules, or starts a 'Reinstall agent' or 'Repair', then switches tabs. Within 10 minutes of a Nightjar UI update the hidden tab reloads silently. The unsaved edits are lost, or the in-flight SSH reinstall or repair is killed partway through.
- **Fix:** Also count `change` events (including checkboxes and radios) as edits, let pages mark themselves dirty explicitly, and track in-flight non-GET requests in client.ts so that no auto-reload happens while any are pending.

#### 120. usePoll drops refresh() while a poll is in flight, so data stays stale after a mutation

`fe-core-3` · frontend-state · verifier: **confirmed** · [frontend/src/hooks/usePoll.ts:25](frontend/src/hooks/usePoll.ts#L25)

- **Status (2026-11-09):** ✅ fixed. Hooks/usePoll.ts discards stale replies and re-runs once.
- **What:** run() returns at once when `inFlight.current === f`. Callers use `refresh()` right after a mutation to show the new state (for example NotificationsProvider.mute, Maintenance, Incidents, Servers, CheckExtras). If a poll for the same fetcher went out before the mutation was applied and is still in flight, the refresh is skipped. That poll then answers with the state from before the mutation, and nothing refetches until the next interval tick, which can be 20-60s.
- **When it bites:** The 20s notification-summary poll is in flight on a slow link when the admin mutes a check. api.mute succeeds and refresh() is a no-op. The in-flight summary arrives without the mute, so the mute button still reads 'Mute' for up to 20s and the admin may mute again. The same happens on the hosts page after editing a server when the 10s poll overlaps.
- **Fix:** When refresh() is called during an in-flight request, set a 'pending' flag and run again as soon as the current request settles (or let the newer request supersede the older one with a sequence number), instead of returning.

#### 121. Cleanup page freezes on the snapshot from 'Check now' or 'Keep' and ignores later polls

`fe-pages-admin-3` · frontend-state · verifier: **confirmed** · [frontend/src/pages/Cleanup.tsx:43](frontend/src/pages/Cleanup.tsx#L43)

- **Status (2026-11-09):** ✅ fixed. Cleanup.tsx clears the snapshot when a poll lands.
- **What:** `const v = view ?? data;`: once scan() or keep() stores a CleanupView in `view`, it is never cleared. The 60-second usePoll keeps fetching, but `data` is never shown again. On a failed scan the code calls refresh(), but a `view` set earlier still hides the result.
- **When it bites:** The user marks one title Keep, then leaves the tab open. The background cleanup pass removes several 'leaving' titles and lists new ones. The page keeps showing the removed titles as 'next pass' with Keep buttons, does not show them under 'Removed lately', and keeps the old 'Freed' totals until the user navigates away. Users act on these stale rows.
- **Fix:** Clear `view` when new poll data arrives (e.g. useEffect(() => setView(undefined), [data])), or after a mutation call refresh() and render only `data`.

#### 122. Malformed URL hash crashes the Docs page (decodeURIComponent in render)

`fe-pages-views-8` · error-handling · verifier: **confirmed** · [frontend/src/pages/Docs.tsx:286](frontend/src/pages/Docs.tsx#L286)

- **Status (2026-11-09):** ✅ fixed. Docs.tsx hashId wraps decodeURIComponent in try/catch.
- **What:** `decodeURIComponent(location.hash.slice(1))` runs during render (line 286) and in an effect (line 276). It throws URIError on malformed percent-escapes. With no error boundary the whole SPA unmounts. The /docs routes are reachable from links in alerts and docs, and the hash is attacker-controlled through a shared link.
- **When it bites:** Someone opens https://host/docs/alerts#%E0%A4%A (or any lone '%'). decodeURIComponent throws `URIError: URI malformed` and the dashboard renders a blank page.
- **Fix:** Wrap the decode in a helper: `const safeDecode = (s) => { try { return decodeURIComponent(s) } catch { return s } }`.

#### 123. Notification inbox 'Show older' ignores filter changes and swallows errors

`fe-pages-admin-6` · frontend-state · verifier: **confirmed** · [frontend/src/pages/Notifications.tsx:158](frontend/src/pages/Notifications.tsx#L158)

- **Status (2026-11-09):** ✅ fixed. Notifications.tsx drops stale replies from more().
- **What:** more() fetches with the `filters` captured when it was clicked and then appends unconditionally with `setItems(old => [...old, ...r.items])`. It does not check whether the filters changed in the meantime, and it has no catch, so a failure becomes an unhandled rejection with no message. Separately, the reload triggered by `latest` (lines 142-146) keeps only old items older than the oldest fresh item. If more than a page of alerts arrives between bell updates, the ids between the two pages are never shown, because 'Show older' continues from the bottom.
- **When it bites:** The user clicks 'Show older' under 'Problems', then switches to 'Info' before it returns. The first-page fetch for Info resolves, then the older Problems page is appended under the Info list, mixing severities. During an alert storm, a gap of notifications silently disappears from the inbox.
- **Fix:** Drop a more() response if the filters changed (keep a request token or compare a ref), and add a catch that sets the error. When the fresh page does not overlap the loaded list, replace the list instead of merging.

#### 124. Agent token edit field is a plain text input

`fe-pages-admin-9` · security · verifier: **confirmed** · [frontend/src/pages/Servers.tsx:593](frontend/src/pages/Servers.tsx#L593)

- **Status (2026-11-09):** ✅ fixed. Servers.tsx new-token field is type=password.
- **What:** EditServer's 'New token' field, which holds the agent's shared secret, is `<input className="field mono">` with no type="password" and no autoComplete="off". Elsewhere secrets use password inputs with autocomplete off (Settings, Setup, UpdateForm). The browser can store the token in its form-autofill history and show it on screen.
- **When it bites:** An admin pastes a new agent token on a shared or screen-shared machine. The token is visible in clear text, and the browser keeps it in autocomplete history, where it can be offered in other fields later. Anyone with the token can query the agent.
- **Fix:** Use type="password" (with a show toggle if needed) plus autoComplete="off" or "new-password" on the token inputs.

#### 125. SSH login fields overwrite the user's edits with a stale `login` after async calls

`fe-pages-admin-7` · frontend-state · verifier: **confirmed** · [frontend/src/pages/Servers.tsx:749](frontend/src/pages/Servers.tsx#L749)

- **Status (2026-11-09):** ✅ fixed. Servers.tsx uses functional updaters for login.
- **What:** SSHLoginFields builds updates as `onChange({ ...login, ... })` from the `login` prop captured at call time. The mount effect (line 749) does this after api.sshKeys() resolves, and `added` (line 762) does it after api.newSSHKey or importSSHKey resolve. The User, Port and Host inputs stay editable during these requests.
- **When it bites:** In 'Add a server' over SSH, the user types the user 'admin' and port '2222' while 'Looking for keys…' is still showing. When the keys arrive, the effect writes back the original login (root/22) with the first key, and the typed values are lost. The same happens when the user edits fields while 'Make key' runs. The server is then saved and tested as root@:22, which may fail or log in as an unintended account.
- **Fix:** Have the parent accept a functional updater (e.g. `onChange(l => ({ ...l, key }))` using setLogin), or keep the latest login in a ref.

#### 126. Clicking a check name pushes two identical history entries

`fe-pages-views-6` · frontend-state · verifier: **confirmed** · [frontend/src/pages/Services.tsx:20](frontend/src/pages/Services.tsx#L20)

- **Status (2026-11-09):** ✅ fixed. Services.tsx/Overview.tsx row clicks ignore links.
- **What:** ServiceRow (and the Overview services table at Overview.tsx:156-161) puts a <Link> inside a <tr onClick={() => navigate(href)}>. React Router 7's Link handler calls preventDefault and navigate(push), but it does not stop propagation. The row's onClick then runs a second navigate(href), which is also a push because useNavigate doesn't auto-replace.
- **When it bites:** On /services the user clicks a check's name. History becomes /services → /services/web → /services/web. Pressing Back once leaves the user on the same detail page, so it takes two Backs to return to the list.
- **Fix:** Call `e.stopPropagation()` in the Link's onClick, or have the row's onClick ignore clicks whose target is inside an <a>. Alternatively navigate only from the row and render the name as plain text.

#### 127. Finder 'Add an uptime check' / 'Add a torrent' do nothing when already on that page

`fe-pages-views-11` · frontend-state · verifier: **confirmed** · [frontend/src/pages/Services.tsx:71](frontend/src/pages/Services.tsx#L71)

- **Status (2026-11-09):** ✅ fixed. Services.tsx/QBittorrent.tsx reopen the form on hash or ?add=1.
- **What:** Services reads `location.hash === '#add'` only as the initial value of `adding`. QBittorrent does the same with `?add=1` (QBittorrent.tsx:26, useState initializer). The Finder actions navigate to '/services#add' and '/qbittorrent?add=1', but when the user is already on that route the component doesn't remount, so the form never opens.
- **When it bites:** On /services the user presses Ctrl+K and chooses 'Add an uptime check'. The URL changes to /services#add and the Finder closes, but no form appears. The same happens on /qbittorrent with 'Add a torrent'.
- **Fix:** Sync from the location in an effect, e.g. `useEffect(() => { if (location.hash === '#add') setAdding(true) }, [location.hash])` and likewise for `params.get('add')`.

#### 128. Setup checks step keeps already-added targets in `picked`, so the button count is wrong

`fe-pages-admin-8` · frontend-state · verifier: **confirmed** · [frontend/src/pages/Setup.tsx:230](frontend/src/pages/Setup.tsx#L230)

- **Status (2026-11-09):** ✅ fixed. Setup.tsx count uses only targets still picked.
- **What:** `picked` is set once from the first suggestions. After add() and refresh(), the added targets drop out of st.checks.suggestions but stay in `picked`. picked.size, which drives the button label and the disabled state, no longer matches what is ticked.
- **When it bites:** Three suggestions are shown and the user adds two of them. After the refresh, one suggestion remains unticked, yet the button reads 'Add 3 checks' and is enabled. Clicking it adds nothing and shows 'Added 0 checks'.
- **Fix:** Base the count on `suggestions.filter(x => picked.has(x.target)).length`, or reset `picked` when suggestions change.

#### 129. One-click Restart for any systemd unit, including the monitor itself, with no confirmation

`fe-pages-admin-10` · data-safety · verifier: **confirmed** · [frontend/src/pages/SystemServices.tsx:126](frontend/src/pages/SystemServices.tsx#L126)

- **Status (2026-11-09):** ✅ fixed. SystemServices.tsx confirms restarts of the self unit and the CAREFUL list.
- **What:** Stop asks for confirmation and is hidden for the monitor's own unit (`u.name !== self`). Restart and Start fire immediately from the row button for every unit, including ssh, docker, networking and Nightjar's own service.
- **When it bites:** A mis-click on 'Restart' on the docker row restarts every container. On the 'ssh' or 'networking' rows it can drop the admin's remote session. On the Nightjar row the request dies mid-response, and the user sees 'Could not restart nightjar' while the monitor restarts.
- **Fix:** Ask for confirmation, or at least do so for the self unit and for a list of critical units (ssh, docker, networking, systemd-*). For the self unit, handle the restart like the settings flow does with waitForRestart.

#### 130. Update-all progress compares the browser clock with server run timestamps

`fe-pages-admin-4` · correctness · verifier: **plausible** · [frontend/src/pages/Updates.tsx:225](frontend/src/pages/Updates.tsx#L225)

- **Status (2026-11-09):** ✅ fixed. Updates.tsx compares against the server-side run.at.
- **What:** itemState decides whether a target's last run belongs to this batch with `run.at >= started - 5`. Here `started` is `Date.now()/1000` from the browser (line 437) and `run.at` is the server's clock. Any skew of more than about 5 seconds gives wrong states.
- **When it bites:** The browser clock is 30 s ahead of the server. A quick apt-package update finishes and `available` becomes false, but run.at < started-5, so the batch lists it as 'Nothing to install' and the summary says '0 updated'. If the browser is instead minutes behind, last night's failed auto-update counts as this batch's run. Items show 'Failed' before they start and the batch can report 'finished' early.
- **Fix:** Use a server-provided timestamp for `started`, e.g. return the server time from applyUpdate or the updates response, or match runs by an id returned when the update is queued.

---

## Fix status (2026-11-05)

All 130 issues now have a fix in the working tree, which is not committed. Twelve agents did the work, one per area of the code, each limited to its own files. A separate reviewer then checked each area's changes, and the agent fixed whatever that reviewer raised. Changes that spanned two areas (for example a backend API change plus its UI, or docs) were applied in a final pass.

**Verification after fixing:** `gofmt -l` clean · `go vet ./...` clean · `go build` of both binaries · `go test ./...` passes · `go test -race ./...` passes · `tsc -b` clean · `vitest` 153 tests in 4 files pass (was 112 in 1) · `vite build` succeeds. Regression tests were added in `backend/test/*` and in `frontend/test/{core,admin,views}.test.tsx`.

**Re-check (2026-11-06).** Six independent reviewers re-read the current code for every finding, judging the logic rather than these notes. 123 of 130 were fully fixed. Seven were only partly fixed and are now finished (numbers as in "All findings" above):
- **2** Log cleanup (emptying every log, aggressive trimming) followed the root filesystem's fullness even when /var/log is on its own partition. It now follows the fullness of the filesystem that holds the logs (`logsPercent`, monitor/disk.go).
- **11** Reinstalling an agent from before `AGENT_FIREWALL` was always written, installed with `-firewall=false`, opened port 9101 to everyone. A settings file with no `AGENT_FIREWALL` line now leaves the firewall as it is, and installs now write `none` when there was nothing to open. Test: `TestAReinstallKeepsTheSettingsAndChoicesOfTheFirstInstall`.
- **18** Archives with stacked suffixes (logrotate `dateext` + `compress`, such as `auth.log-20240115.gz`) were not recognised as security logs and could be deleted. Rotation suffixes are now stripped until none is left. Test: `TestSecurityArchivesAreNeverDeleted`.
- **33** After a failed delivery, the next identical alert within the hour (a second outage after a recovery) only updated the first record and was missing from history. Any later notification about the same subject now ends that retry link. Test: `TestASecondOutageAfterARecoveryIsItsOwnRecord`.
- **43** `systemctl stop` during an update waited far past `TimeoutStopSec`, so systemd killed apt and skipped closing the database. While an update is running, Nightjar now asks systemd for more time (`EXTEND_TIMEOUT_USEC`, which needs `NotifyAccess=main` in the unit), and background work gets 15s instead of 5s.
- **60** Behind a plain `proxy_pass` with no `DASHBOARD_URL`, sign-in and every change got 403. A request the browser marks `Sec-Fetch-Site: same-origin` (which pages cannot set) is now accepted. Test: `TestOriginCheckBehindAProxy`.
- **93** Until a hung plugin's breaker opened, each alert stalled the monitoring loop for 20s, so two in one cycle set off the 30s watchdog (and auto-restart). The hub now keeps the watchdog fed while it waits on slow channels (`Hub.Waiting`). Test: `TestTheHubSaysItIsWaitingOnASlowChannel`.

Smaller leftovers the reviewers noted were also closed:
- **109** A check round is now marked running when its job is taken, not a moment later.
- **16** A link planted at an nginx log's name is removed before nginx restarts.
- **30** The kernel crash check keeps the watchdog fed.
- **67** `ssh` no longer inherits the monitor's secrets.

`go vet`, `go test ./...` and `go test -race ./...` pass.

**Behaviour changes worth knowing:**
- Turning on 2FA now asks for the password. 2FA cannot be set up again while it is on: turn it off first.
- Adding an SSH key with a password now shows the server's host-key fingerprint, and you must confirm it before the password is sent. Confirmed keys are kept in `known_hosts.confirmed`.
- The DDoS defender no longer blocks addresses for half-open (SYN) connections, because their source is easy to forge. It blocks IPv6 per /64. It removes its iptables chains on stop and on agent uninstall.
- Disk cleanup runs only when the root filesystem is the full one, and it cleans only on the filesystem that is full.
- Media cleanup no longer falls back to fuzzy title+year matching for torrents it deletes along with their files.
- `INACTIVE_TORRENT_AUTO_REMOVE` removes only torrents that finished downloading.
- A "Without secrets" backup strips agent tokens and plugin secrets. Restoring such a backup keeps the tokens already stored on this machine.
- The systemd unit's `TimeoutStopSec` is now 90s. Shutdown waits for an apt/dpkg update that is running, instead of killing it.
- Restarting Nightjar from System services replies first and then restarts.
- The unit file changed, so run `nightjar -install-systemd` again on existing installs to pick up `TimeoutStopSec`, `NotifyAccess=main` and the user fix.

| # | Finding | Severity | Status | What changed |
|---|---|---|---|---|
| 1 | `host-collect-1` edacErrors dereferences a nil DirEntry when the EDAC sysfs root is missing, so the monitoring cycle panics | high | fixed | [host] backend/internal/kernel/monitor.go: replaced the WalkDir callback (which ran d.Name() before checking err, so it panicked when the EDAC root was missing) with an exported EdacCounts(root). It uses ReadDir/ReadFile, so a missing /sys/devices/system/edac/mc just returns no counts. Regression test: test/kernel/edac_test.go TestNoEdacDriverMeansNoCounts. |
| 2 | `host-collect-2` Rotated-log removal and log truncation follow symlinked intermediate directories (TOCTOU), allowing arbitrary root file deletion or truncation | high | fixed | [host] backend/internal/cleanup/logs.go: every log delete and open now goes through os.Root, via the new rootFor/removeLog/openRegular helpers. The root is the log dir when the file is inside it, otherwise the file's own dir. A directory swapped for a symlink that points out of /var/log after the walk can no longer redirect an unlink or truncate. removeLog Lstats through the root and refuses anything that is not a regular file. IsRotatedLog now also needs the name, with its rotation suffix stripped, to look like a log (log keywords plus dmesg/debug), and rejects .so/.so.N/.ko names and Samba-s… |
| 3 | `host-collect-3` Disk cleanup ignores the full mount and empties every /var/log file when an unrelated partition (or the total across disks) is full | high | fixed (both halves: cleanup/disk.go now cleans only targets on the full filesystem via sameFilesystem/onMount) | [monitor] This is only the monitor half; host owns the cleanup half. internal/monitor/disk.go: diskCleanup now triggers from the root filesystem's own percent (rootPercent: the '/' partition, otherwise collect.DiskUsage("/"), otherwise 0), not TotalDisk.Percent. Its messages also show the root percent. partitionMaintenance no longer cleans '/' (diskCleanup covers it, which avoids a double cleanup). Its gated notifications are covered under monitor-core-8. It still calls EmergencyClean and CleanDiskSpace(p.Mountpoint) for other partitions. That relies on host making cleanup/disk.go clean only t… |
| 4 | `integrations-1` Media cleanup matches torrents by title+year and deletes them with their files, which can wipe collection or pack torrents holding other watched movies | high | fixed | [integrations] backend/internal/mediacleanup/mediacleanup.go: a Downloads (arr history) error no longer falls back to name matching. The item is marked unsure for the pass, gets no torrents, and when due is skipped with a Reason saying the history could not be read and will be retried. Each attached Torrent now has an unexported withFiles flag. Torrents matched by arr hash keep deleteFiles=true. Torrents matched only by name delete their files only when the new holdsOnly() confirms the torrent holds just this item: its content_path is the item's file or a folder containing all of the item's fi… |
| 5 | `updates-apps-1` Repair script timeout cannot fire when the script leaves a background child; repair and shutdown hang forever | high | fixed | [updates] backend/internal/repair/repair.go runScript: the script's stdout/stderr now go to an unlinked temp file instead of a pipe, so cmd.Run returns as soon as the script exits even if a program it started in the background keeps the fd. Pipes were not kept because closing them would SIGPIPE the restarted daemon. The script also runs in its own process group (Setpgid), and cmd.Cancel SIGKILLs the whole group when scriptTimeout fires. Output is read back with a new tail() helper, which keeps the last maxOutput bytes as before. Regression test: TestScriptThatStartsAProgramFinishes in backend/… |
| 6 | `x-contract-1` Bodiless POST/PUT calls are always rejected with 415: Keep (cleanup), Check now, Send report and Try repair never work | high | fixed | [fe-core] frontend/src/api/client.ts: request() now sends a '{}' JSON body with Content-Type: application/json for every method except GET and DELETE when the caller gives no body, so fromDashboard no longer returns 415. sendReport, repairCheck, cleanupScan and cleanupKeep(key, true) also pass body: {} explicitly. DELETE still goes out without a body. Regression test in frontend/test/core.test.tsx checks the method, Content-Type and body of all four calls, plus the un-keep DELETE. |
| 7 | `x-security-1` "Without secrets" backup still contains agent tokens (remote root) and plugin secret settings | high | fixed | [api] internal/api/backup.go: when secrets=0, makeBackup strips the 'token' from every remote.hosts entry. In plugins.state it keeps only settings whose manifest type is known not to be secret (from s.ctx.Plugins.List()); settings of plugins it cannot look up are dropped too. On restore of a backup with secrets=false, keepSecrets puts back the agent tokens already stored here (matched by host id) and any plugin settings the backup left out. Review follow-up: In backend/internal/api/backup.go, keepSecrets now keys the current hosts by id, and puts a token back only when the incoming host's addr… |
| 8 | `api-auth-1` clientIP trusts a client-supplied X-Real-IP through a local reverse proxy, so the login throttle can be bypassed and the owner can be locked out on purpose | medium | fixed | [api] internal/api/server.go: clientIP was replaced by clientIPs(r). Behind a loopback proxy it takes X-Real-IP and the last X-Forwarded-For hop (the last header value's last element). Each must parse with net.ParseIP, duplicates are dropped, and it falls back to RemoteAddr when neither parses. Every throttle check and failure now uses all of these addresses, so making up the header the proxy does not set (nginx as documented passes XFF through, Caddy/cloudflared pass X-Real-IP through) no longer gets around the limit. The documented nginx setup keeps working. When the map is full, eviction no… |
| 9 | `api-auth-3` http.Server WriteTimeout of 30s is shorter than several handlers' own budgets (SSH up to 5 min, unit control 90s, cleanup 5 min, repair or plugin 2 min), so finished actions return no response | medium | fixed | [api] api part: server.go adds slow(budget, h), which raises the connection's write deadline with http.NewResponseController(...).SetWriteDeadline(budget+30s). It is applied to controlUnit 90s, cleanupScan 5m, repairCheck 3m, tryPlugin 2m, runHostSSH/saveHost/setHostSSH (sshBudget 5m30s) and authorizeSSHKey 1m. compress.go: gzipWriter gets Unwrap() so ResponseController can reach the connection. hosts.go: runHostSSH and authorizeSSHKey now run on context.WithoutCancel(r.Context()), within remote's own timeouts, so a closed tab or client abort cannot cut a reinstall off halfway. The frontend cl… |
| 10 | `api-auth-6` collectStats clears Cmdline, RSS and VMS on process slices shared with the scanner's cache (data race, and alerts lose data) | medium | fixed | [api] internal/api/server.go collectStats: TopCPU and TopMemory are copied with slices.Clone before Cmdline, RSS and VMS are cleared, so the scanner's cached entries that alerts read stay untouched. This removes the data race and the missing alert data. |
| 11 | `ddos-firewall-1` Any unauthenticated TCP connection to sshd counts as an SSH login: the attacker is trusted by the DDoS defender, and the dashboard refuses rules that would block it | medium | fixed | [ddos] An open TCP connection to sshd no longer counts as an SSH login. New file internal/ddos/ssh.go adds SSHLogins(proc). It counts a login only when the socket is held by an sshd process whose title shows a finished login ("sshd: user@tty" or "sshd-session: user@tty"), found by matching socket inodes in /proc/<pid>/fd against /proc/net/tcp*. The socket's local port must also be a listening port, so connections a session forwards are not counted. ParseTCP no longer sets Peer.SSH. readTable(proc) sets it from SSHLogins, which feeds the defender's trust list and its never-block check. firewall… |
| 12 | `ddos-firewall-3` Startup cleanup can write a literal "?" into the nginx include file, breaking nginx's config | medium | fixed | [ddos] defend.go no longer uses "?" as a placeholder in d.web. A new webStale bool marks a leftover deny file from an earlier run, and writeWebLocked retries while it is set. Because previous is "" in that case, a failed nginx -t removes the file instead of writing "?" back. Test: TestALeftoverNginxFileIsNeverReplacedWithJunk. |
| 13 | `ddos-firewall-4` In big HTTP floods, per-source request counts are compared to an extrapolated total, so flooding sources escape blocking | medium | fixed | [ddos] read.go LogTail.Requests now counts per-address requests in a local map. When more than 8 MiB arrives it scales those counts by the same factor (size/parsed) as the total, so each address keeps its true share and rate. Test: TestInABigFloodEachAddressKeepsItsShareOfTheRequests (24 MiB from 10 bots, each about 10%). |
| 14 | `ddos-firewall-5` SSH ports are read once from the main sshd_config only, so default-drop can lock SSH out and SSH sessions go unrecognised | medium | fixed | [ddos] Replaced the package-level sshPorts, which was read once at startup, with SSHPorts(proc), which reads fresh on every call. It combines three sources: sshd_config with Include globs (relative to the config's folder), Port lines with trailing comments or the Keyword=value form, and ListenAddress host:port / [v6]:port (Port is ignored when every ListenAddress names its port); ssh.socket ListenStream drop-ins in /etc/systemd/system and /run/systemd/generator; and the LISTEN sockets sshd processes hold in /proc. It also exports SSHConfigPorts(path). firewall.openLocked uses SSHPorts(m.proc()… |
| 15 | `fe-pages-admin-1` Setup's Plex step deletes the saved Plex token when it is not retyped | medium | fixed | [fe-admin] frontend/src/pages/Setup.tsx PlexStep values(): PLEX_TOKEN is sent only when the detected token is used ('@detected') or a token was typed; an empty token is left out of both the test and the save payloads, so the server no longer deletes the stored token. |
| 16 | `fe-pages-admin-2` Maintenance edit form keeps the first window's data when Edit is clicked on another window, so saving overwrites the wrong window | medium | fixed | [fe-admin] frontend/src/pages/Maintenance.tsx: <WindowForm key={editing.id \|\| 'new'} …>, so clicking Edit on another window (or switching from 'Plan maintenance' to Edit) remounts the form with that window's fields and id instead of saving over the first one. |
| 17 | `gap-1` WEB_AUTH_TOKEN can be brute-forced with no throttle through public /api/config and /health | medium | fixed | [api] internal/api/server.go: public /api/config, /health and the config's appearance now go through access(r), the same path as auth(). A wrong token there counts as a failure, and while the client is locked out the token is not looked at and the answer is 'not authorized'. config computes authorized once and passes it to publicAppearance(signedIn) (appearance.go), so a request is not counted twice. |
| 18 | `host-collect-10` Security and audit logs are protected from truncation but their rotated archives (auth.log.N, audit.log.N, wtmp.1, btmp.1) are deleted | medium | fixed | [host] backend/internal/cleanup/logs.go: isProtectedLog now also matches rotated names by stripping the rotation suffix (auth.log.2.gz, btmp.1, wtmp.1, secure-20240115), as well as anything under an audit directory. RotatedLogs skips the audit directory and protected archives, so CleanupRotated and Survey never list or delete them. Clean's rotated branch also keeps protected archives whole. Samba's live log.<ipv4> files no longer count as rotated. Test: TestSecurityArchivesAreNeverDeleted. |
| 19 | `host-collect-11` DirSize/LargestSubdirs cross filesystem boundaries and can block the monitoring loop indefinitely on network mounts | medium | fixed | [host] cleanup part. In backend/internal/cleanup/disk.go, DirSize now walks in a goroutine and returns at the deadline even if a stat hangs on a dead NFS mount: the stuck walk is abandoned and the partial size is returned. timeout 0 still waits for the whole walk. LargestSubdirs reads mount points and their devices from /proc/self/mountinfo, so it never stats a mount. When parent is itself a mount point, it skips subdirectories and nested mounts on another device, so a NAS under /mnt is not counted as inside /. It also caps the whole call at 2x perDirTimeout. DirSize used directly (the media d… |
| 20 | `host-collect-4` SetupNginxLogs hands the nginx log dir to www-data, then creates and chowns files through symlinks as root (CVE-2016-1247 pattern) | medium | fixed | [host] backend/internal/cleanup/disk.go SetupNginxLogs: no longer chowns the nginx log directory to the web user. Log files are created through os.OpenRoot(NginxLogDir) with O_CREATE\|O_EXCL\|O_NOFOLLOW at mode 0640, so an existing file or a planted symlink is left alone. Ownership is set with f.Chown on the open descriptor, using uid/gid from os/user lookups instead of `id` and `chown`. NGINX_SERVICE_NAME is now checked against serviceNameRE before systemctl restart. No unit test, because the function restarts nginx via systemctl. Review follow-up: backend/internal/cleanup/disk.go: new reclai… |
| 21 | `host-collect-5` smartctl 'device open failed' is classified as 'asleep', so a dead or vanished drive keeps its last good reading and never alerts | medium | fixed | [host] smart part. In backend/internal/smart/smart.go, asleep() now means standby only when smartctl's message says standby or sleep. The exit-status-bit-2 fallback is gone, since that bit also means 'device open failed' or 'no IDENTIFY'. In readOne, an unreadable reading (no model and no verdict) now returns Error=smartctl's message with Asleep=false and health computed from no data ('unknown' instead of the stale 'good'). It keeps the previous Model/Serial/Capacity/Rotational and the previous Checked time, so the stored baseline is not overwritten with zeros. Test: TestADriveThatStopsAnsweri… |
| 22 | `host-collect-6` EDAC counter keys collapse to 'edac_<file>' for every controller and csrow, causing false daily memory-error alerts | medium | fixed | [host] backend/internal/kernel/monitor.go: EDAC counters are now read only from each controller's top-level total (/sys/devices/system/edac/mc/mcN/ce_count and ue_count). They are keyed per controller (edac_mc0/ce_count), and only the .../edac/mc root is read, once, in sorted order. csrow/dimm/noinfo files are ignored. Source is always 'Memory Controller mcN'. load() drops the old collapsed keys (edac_ce_count and similar) from LastEdacCounts and LastAlertTime. Test: TestEdacCountsAreKeptPerController. |
| 23 | `host-collect-8` kernelLogCrashes has no time filter: old crashes re-alert after every restart or seen-cache reset, and one Oops is counted up to 5 times | medium | fixed | [host] backend/internal/kernel/monitor.go: kernelLogCrashes now filters each line by its own syslog timestamp. The new exported SyslogTime handles RFC 3339 and the traditional 'Jan _2 15:04:05' format with year inference, and the scan stops at the first line older than the cutoff. Old crashes left in the tail therefore no longer re-alert after a restart or a reset of the seen map. journalCrashes now emits one event per journal entry rather than one per matching keyword. CheckCrashes deduplicates crashes across journal, dmesg and kern.log with newCrashes, comparing the message text after stripp… |
| 24 | `host-collect-9` Host bandwidth sums every interface including loopback, bridges and veth, inflating rates and causing false network alerts | medium | fixed | [host] backend/internal/collect/host.go NetworkIO: reads per-interface counters and sums only physical interfaces (those with /sys/class/net/<if>/device), which leaves out lo, docker0/br-*, veth, VLANs and tunnels. If there are none it falls back to the default-route interface from /proc/net/route, and failing that to every interface except lo. Returns the same IO struct, so callers are unchanged. Smoke test: test/collect/host_test.go TestHostCountersRead. |
| 25 | `integrations-2` Keep clicked during a cleanup pass does not stop the removal, and the deleted item then shows as Kept | medium | fixed | [integrations] mediacleanup.go: right before each removal, scan takes m.mu, reloads the stored Kept set and skips the item if it was kept during the pass. Otherwise it adds the key to a new Manager.removing set (guarded by mu). Keep() refuses keys in that set with "it is being removed right now", which the API returns as a 400. A key leaves the set if its removal fails, and the whole set is cleared under the same lock as the final save, so a removed item can no longer turn into Kept with no record. Test: TestKeepDuringAPass covers a Keep before the removal (spared) and a Keep during the qB del… |
| 26 | `integrations-3` Renaming a check while it is running leaves it marked running forever, so it never runs again | medium | fixed | [integrations] backend/internal/services/manage.go and monitor.go, with no change to the Claim/Record API. Renaming a check while it runs records oldName->newName in a new Monitor.renamed map, and earlier renames in a chain are re-pointed to the new name. Record follows that map when no state has the name, or when the state found is not running (for example a new check that has taken the old name). This clears running on the renamed check and records the result under its new name. A type or target change during a run now keeps running=true and sets a new State.stale flag. The new target does n… |
| 27 | `integrations-5` EverWatched silently stops at 100,000 history entries, so cleanup treats items watched beyond that as never watched | medium | fixed | [integrations] backend/internal/plex/client.go EverWatched: the silent 100k cap is gone. Paging continues until a short page, up to maxWatchHistory (2,000,000 entries). Hitting that limit returns an error instead of a partial map. It also counts the entries read, and returns an error if fewer were read than the first page's totalSize (entries deleted mid-read shift the pages). The cleanup scan then stops, as it already does on a history error. Test: TestEverWatchedReadsAllOrFails. |
| 28 | `monitor-core-4` Long synchronous disk work with no heartbeat makes the 30s watchdog fire false 'Nightjar stopped checking' alerts and can restart Nightjar mid-cleanup | medium | fixed | [monitor] internal/monitor/context.go: withTimeout now takes the *Context and beats the heartbeat every 5s while it waits. This is safe because it always returns within its timeout. All call sites are updated: disk.go, logs.go, torrents.go, resources.go. Context.Heartbeat now also beats the FreezeMonitor, so a slow but live cycle no longer reads as a 'System Freeze'. The unbounded LargestSubdirs calls (alertPartitions, alertTotal, the media alert) go through a new handler.largest helper with a 1-minute total budget under withTimeout. The media DirSize also runs under withTimeout, and its parti… |
| 29 | `monitor-core-5` qBittorrent tracking cap evicts live torrents every check when more than 1000 exist, resetting stall and ratio state | medium | fixed | [monitor] internal/monitor/torrents.go: removed the maxTrackedTorrents cap, the seenAt field and the sort import. Removing torrents that are no longer listed already keeps the map no bigger than qBittorrent's own list, so live torrents are never evicted and never reset. Regression test: TestManyTorrentsKeepTheirState (1500 stalled torrents are all reported). |
| 30 | `notify-incidents-1` Incident split by the 15-minute Join gap is never closed (phantom open incidents) | medium | fixed | [notify] backend/internal/incidents/incidents.go Build: `open` is now a subject -> *Incident map that persists across the whole scan (finish no longer resets it). A re-alert of a subject that is still down joins the incident it went down in, with no Join split. A recovery closes whichever incident holds that subject, and that incident gets Ended once it holds nothing open. Incidents that are actually separate still split on the Join gap. Regression test TestALongOutageClosesWhenItRecovers covers the 0/300/900/2100/3000 re-alert pattern and a recovery that arrives during a later incident. |
| 31 | `notify-incidents-2` Hub's failed-delivery map collapses later alerts with the same title into an old record, dropping them from history | medium | fixed | [notify] backend/internal/notify/hub.go deliver: failed records are now keyed by area+subject+title+body and stored with a timestamp (new `failure` type, exported `RetryWindow = time.Hour`). Only the same alert sent again within the window counts as a retry and updates the old row. Anything else inserts a new row, and expired entries are pruned on every delivery. The existing retry-in-place test still passes. Added TestALaterAlertWithTheSameTitleIsNotARetry. |
| 32 | `notify-incidents-3` Webhook URLs and bot tokens leak through delivery errors into notification history, test results and logs | medium | fixed | [notify] notify.go: new withoutURL() unwraps *url.Error in base.deliver, and delivery.record stores the logx.Redact'ed message. discord/client.go: Webhook.send unwraps *url.Error after the retry loop, and Webhook.failed redacts. bot.go: Bot.call unwraps too. logx.go: secretPatterns now also cover ptb./canary./versioned Discord webhook URLs, Telegram `/bot<id>:<token>` and hooks.slack.com/{services,workflows,triggers}/... Tests: TestNetworkErrorsHideTheURL (Telegram plus generic webhook), TestNetworkErrorsHideTheWebhookToken (Discord), TestURLsThatAreSecretsAreMasked (logx). |
| 33 | `remote-agent-2` Reinstall rewrites /etc/nightjar-agent.env, silently dropping AGENT_TRUSTED / AGENT_DEFEND and re-opening the firewall the admin declined | medium | fixed | [remote] backend/internal/agent/install.go: replaced the private settings struct with an exported Settings (ParseSettings, Settings.Env, Settings.Reinstall); Env() remains as a wrapper. A reinstall now (1) keeps every line install does not own, such as AGENT_TRUSTED, AGENT_DEFEND and the admin's comments, verbatim, (2) records -firewall=false as AGENT_FIREWALL=off and -smart=false as AGENT_SMART=off and honours both on reinstall instead of the flag defaults, and (3) keeps the earlier AGENT_FIREWALL when OpenPort now returns "", so uninstall still closes the port. Uninstall skips ClosePort for … |
| 34 | `storage-config-main-3` LOG_FORMAT=json is always rejected, so JSON logging can never be turned on | medium | fixed | [storage] backend/internal/config/fields.go choice(): it no longer upper-cases the value. Aliases are looked up by the upper-cased value, options are matched case-insensitively with strings.EqualFold, and the option's own spelling is stored. LOG_FORMAT=json, JSON or ' Text ' now give json/text. LOG_LEVEL and severity behave as before. Test added. |
| 35 | `storage-config-main-5` Notify plugins added or rescanned after startup never receive alerts, though the UI shows them as working alert channels | medium | fixed | [storage] backend/internal/plugins/plugins.go: added Manager.NotifyChanged. Scan calls it when the set of plugins providing notify (by id and name) changes. backend/cmd/nightjar/main.go run() sets it to log and call requestRestart, the same restart path settings changes use, so a notify plugin added, renamed or removed via Look again gets its hub destination after the automatic restart. Enabling or configuring an existing notify plugin already worked without a restart. Test TestFindingANewAlertPluginIsReported added. Telling the user about the restart in the UI is listed in needs_followup. |
| 36 | `storage-config-main-6` Firewall and DDoS-defense state read with db.Meta: an unavailable or erroring DB is treated as "no rules" and "defense on", which strips the firewall rules from the kernel | medium | fixed | [storage] storage: added Store.MetaValue(key) (string, error), which returns ErrUnavailable with no DB and passes on real scan errors; Meta now wraps it. The DSN carries _pragma=busy_timeout(5000), so a briefly held lock no longer makes Open fail. main.go: the firewall rules are read with MetaValue, and on error ctx.Firewall stays nil and Start is skipped, so the kernel rules are left as they are and not flushed (the API already handles a nil Firewall). The DDoS On() now uses defending(db), which keeps the last good reading and stays off until one succeeds, instead of treating a failed read as… |
| 37 | `storage-config-main-8` INSTALL_SYSTEMD=true in .env makes the installed service crash-loop instead of monitoring | medium | fixed | [storage] backend/cmd/nightjar/main.go: the -install-systemd flag always installs. The INSTALL_SYSTEMD env/.env switch is honoured only when INVOCATION_ID is unset, i.e. not started by systemd, so the installed service goes on to run() instead of crash-looping. env.recommended's INSTALL_SYSTEMD comment now says it installs when started by hand, then exits, and that the service ignores it. |
| 38 | `updates-apps-2` Restart or shutdown during an update exec()s over a running apt/dpkg after 1 minute, killing it with SIGPIPE | medium | fixed | [updates] backend/internal/updates/manager.go: Wait now sets a stopping flag under mu, and work() drops every job dequeued after that, so nothing new starts once shutdown begins. This also fixes the racy WaitGroup Add/Wait. When an update is running, Wait waits for as long as the update can take (opt.Timeout + probeTimeout + 5m) instead of the caller's minute, so an in-process exec restart no longer cuts apt/dpkg off. m.applying is now set in the same critical section as running.Add, so Wait cannot miss an update that is just starting. Added a probeTimeout constant to replace the two hard-code… |
| 39 | `updates-apps-3` Update command timeouts do not work for wrapped/multi-process commands (runuser, sudo, sh -c scripts): the single worker can hang indefinitely | medium | fixed | [updates] backend/internal/updates/exec.go Exec: commands now run in their own process group. cmd.Cancel kills the group (-pgid), and cmd.WaitDelay=5s makes Run return even when a descendant (git under runuser/sudo, curl under sh -c) still holds the output pipes. A command that exits successfully but leaves a background child holding the pipes gets exec.ErrWaitDelay, which is now treated as success. Regression test: TestExecTimeoutStopsWhatTheCommandStarted (a timeout on `sh -c 'sleep 30; true'` returns fast; `sleep 8 & echo done` counts as success). |
| 40 | `updates-apps-5` Service installed via sudo runs as SUDO_USER with NoNewPrivileges=true, so every root operation (sudo -n fallback) fails | medium | fixed | [updates] backend/internal/systemd/service.go: Install no longer takes SUDO_USER and always writes a root unit (User= is not named, which keeps CAP_SETUID as the existing comment explains). UnitContent leaves out NoNewPrivileges and RestrictSUIDSGID when it is given a non-root user, because that user reaches root through sudo; the other hardening stays. Regression test: TestAUnitForAnotherUserCanStillUseSudo in backend/test/systemd/unit_test.go. The existing root-unit test is unchanged and passes. |
| 41 | `x-contract-6` A broken plugin is listed with "provides": null and crashes the Plugins page and the check form's plugin picker | medium | fixed | [storage] backend/internal/plugins/plugins.go Scan: a plugin whose Provides is nil (no plugin.json, invalid JSON, missing or null "provides") gets []string{}, so GET /api/plugins never sends "provides": null. Test TestBrokenPluginsProvideNothingRatherThanNull added. |
| 42 | `x-security-3` DDoS defender can be made to block arbitrary third-party addresses with spoofed SYNs, and the block drops their replies too | medium | fixed | [ddos] Half-open (SYN_RECV) counts no longer block an address: blockHalfOpen is removed, since a forged source creates those entries and the SYN limit plus SYN cookies already handle them. Block DROPs now come after a conntrack REPLY-direction RETURN, so blocking an address never drops replies to this host's own connections. The resolv.conf name servers (including systemd-resolved's upstreams) and the default IPv4/IPv6 gateways from /proc/net/route and ipv6_route are read each sample into the new Situation.Upstream and trusted. Tests: TestEachKindOfAttackGetsItsDefence (forged-SYN source not b… |
| 43 | `api-auth-10` ChangePassword writes back an admin snapshot taken before a slow hash, undoing concurrent 2FA and recovery-code changes | low | fixed | [api] internal/accounts/accounts.go ChangePassword: after hashing it takes the lock, refuses with ErrWrongCurrent if the admin is gone or its Hash changed in the meantime, then copies the current a.admin and changes only Hash. A concurrent 2FA enable, used recovery code or LastStep change is no longer overwritten. |
| 44 | `api-auth-11` Public /health resets gopsutil's global per-CPU baseline that the monitor loop's CPU readings depend on | low | fixed | [api] internal/api/server.go health: CPU usage now comes from s.ctx.Latest().CPUPercent, the loop's own reading, instead of collect.PerCore(), so /health no longer resets gopsutil's global per-CPU baseline. The cpu check is left out until the loop has a reading. |
| 45 | `api-auth-12` CSRF Origin check compares against r.Host only, so login and all writes fail behind a proxy that rewrites Host | low | fixed | [api] internal/api/writes.go: fromDashboard is now a method using the new s.sameOrigin(w, r). An Origin is accepted when it matches r.Host, or the first X-Forwarded-Host value when the peer is a loopback proxy, or the host in DASHBOARD_URL (s.ctx.Config().DashboardURL). Any other origin still gets 403. Behind a loopback proxy the 403 message also explains how to pass on the Host header. The backup restore route's inline copy of the check now uses sameOrigin too. |
| 46 | `api-auth-2` auth() returns 429 to valid sessions once their IP bucket has 10 token failures, letting anyone who shares the bucket lock out the admin | low | fixed | [api] internal/api/server.go: a new access(r) checks a valid session cookie first and lets it in even while the address is throttled. A token is only compared while the client is not locked out (otherwise lockedOut gives 429), and every wrong token is recorded. auth() switches on that result. authorized(r) now wraps access(r). |
| 47 | `api-auth-5` Public /api/status rebuilds 30 days of incidents on every request without caching, holding the global store mutex | low | fixed | [api] internal/api/incidents.go: publicNotices collects the published notices first and returns early when there are none, without calling listIncidents. Otherwise it reads incident end times from a new endedCache (1-minute TTL, the Server.ends field) instead of rebuilding 30 days of incidents on every GET /api/status. Notice text and edits still show immediately. |
| 48 | `api-auth-8` Login throttle is check-then-act around a 600k-iteration PBKDF2, so concurrent requests bypass it and burn CPU | low | fixed | [api] internal/api/server.go: new passwordTry. Under failMu it checks the limit and counts the try against every client address before the slow check, then waits up to 5s for one of 2 hashing slots (429 when none frees up). finish(failed) frees the slot and gives the try back unless it failed. auth.go: login, createAdmin, changePassword and 2FA enable/disable all use it, so concurrent requests cannot all pass the throttle, and PBKDF2 runs at most 2 at a time. |
| 49 | `api-auth-9` 2FA can be re-enrolled without the password, and /2fa/disable is an unthrottled password oracle | low | fixed | [api] internal/accounts/twofactor.go: EnableTwoFactor(password, code) now checks the password (ErrWrongCurrent) and makes sure it did not change during the check. BeginTwoFactor and EnableTwoFactor refuse with the new ErrTwoFactorOn while 2FA is on, so the authenticator can only be replaced after turning 2FA off (password + code). internal/api/auth.go: enableTwoFactor reads {password, code}. Enable, disable and changePassword now go through passwordTry, so they are throttled and count a wrong password (and a wrong code on disable) as a failure. The disable route is therefore no longer an unthr… |
| 50 | `ddos-firewall-10` IPv6 attackers bypass per-address limits and fill the block table, because limits and blocks are per /128 | low | fixed | [ddos] For IPv6 the defender's per-address SYN and UDP hashlimits add --hashlimit-srcmask 64 (new perSource helper), and connlimit uses --connlimit-mask 64. blockLocked groups IPv6 sources by /64 (new blockTargets), adds up their connections and requests, and blocks the /64 as a network (e.g. 2001:db8::/64). A /64 that contains any trusted address is skipped (new trustList.overlaps), so rotating /128s no longer fill maxBlocks. Rules() accepts CIDR blocks, nginx deny takes CIDRs, and Unblock and pardons work by that key. The URL-escaped path makes the API and agent unblock routes accept it. Tes… |
| 51 | `ddos-firewall-11` Unbounded per-attack source map, re-sorted on every sample | low | fixed | [ddos] watch.go record() caps w.sources at maxSources (2000). Past that it drops the lower half by weight, so memory and the per-sample sort stay bounded. The sort now breaks ties by address so the result is the same every time. No dedicated regression test: the map size cannot be observed through the exported API. Existing attack-record tests still pass. |
| 52 | `ddos-firewall-2` Per-address blocking runs for every attack type, so legitimate heavy web clients are blocked during an unrelated UDP/SYN/volumetric attack | low | fixed | [ddos] defend.go blockLocked now checks each block reason against the attack actually under way. Connection counts only count during a ConnectionFlood or slow-request kinds (http_slowloris/http_slow). Request rate and share only count during an HTTPFlood, when s.Requests is attack-level traffic. With neither method nothing is blocked. The conn_cap measure text now says that addresses holding 300+ connections get blocked. Test: TestAddressesAreBlockedOnlyForWhatTheAttackDoes. Review follow-up: The defend.go fix stands. The user docs (frontend/src/docs/attacks.md) and the demo data are outside t… |
| 53 | `ddos-firewall-6` Outbound connections are counted as attack sources, and blocks drop their replies | low | fixed | [ddos] ParseTCP now records LISTEN sockets and counts an ESTABLISHED connection only when its local port is a listening port. Connections whose port is not yet known are held until the end of the table, so line order does not matter. SYN_RECV entries always count. This machine's own outbound connections no longer count as sources or set MaxPerSource. Rules() also adds '-m conntrack --ctstate ESTABLISHED,RELATED --ctdir REPLY -j RETURN' before the per-address DROPs when conntrack is on (new plan.conntrack field), so a block never drops replies to this host's own connections. Tests: TestTheKerne… |
| 54 | `ddos-firewall-7` Defense chains and firewall chains are never removed when the daemon stops or the agent is uninstalled | low | fixed (finished in the cross-area pass) | [ddos] Done in my paths. New Defender.Stop(ctx) clears the blocks and measures, removes the NIGHTJAR/NIGHTJAR-UDP chains in both families and removes the nginx deny file. Called before Start, it removes what an earlier run left. Watcher.Run calls it on ctx.Done() with context.WithoutCancel(ctx). New firewall Manager.Uninstall(ctx) removes NIGHTJAR-RULES/NIGHTJAR-OUT and their INPUT/OUTPUT jumps in both families and leaves the saved rules alone. Tests: TestStoppingTakesTheDefenceAway and TestUninstallTakesTheRulesOut. Still needed elsewhere (see needs_followup): agent Uninstall must call these,… |
| 55 | `ddos-firewall-8` Applied rules are cached and never re-verified, so a flushed INPUT silently disables the defense and the firewall rules | low | fixed | [ddos] Defender.syncLocked now runs 'iptables -C INPUT -j NIGHTJAR' for each family on every sample even when the rules have not changed. If the jump is missing it logs a warning and reapplies. On the firewall side, Manager.Start now also starts keepInPlace, which every CheckEvery (new field, default 1 minute) checks the INPUT/OUTPUT jumps and compares the rebuilt rules with what was last applied (new m.built field). It reapplies when a jump is missing or the rules changed. The duplicated build logic now lives in wantLocked, takeOutLocked and familiesLocked. Tests: the reload step in TestEachK… |
| 56 | `ddos-firewall-9` Blocking DNS lookups run inside the Defender's mutex | low | fixed | [storage] backend/cmd/nightjar/main.go trustedAddresses: hostname lookups now run in a background goroutine. The first one starts when the defender is built, and later ones start when the 10-minute cache expires. The function the Defender calls under d.mu only returns the cached slice, so no DNS happens under the lock. If a name fails to resolve, its last known addresses are kept, so a DNS timeout during a flood no longer drops watched servers from the trusted list. |
| 57 | `fe-core-3` usePoll drops refresh() while a poll is in flight, so data stays stale after a mutation | low | fixed | [fe-core] frontend/src/hooks/usePoll.ts: if refresh() is called while the same fetcher's request is in flight, it now sets a pending flag instead of being dropped. When that request settles, its reply is discarded (it may be from before the change), loading stays true and the hook fetches again at once. Interval ticks still skip while a request is in flight, so polling cadence is unchanged. refresh is now a stable wrapper that ignores arguments. Test in core.test.tsx. |
| 58 | `fe-core-4` A failed /api/config at startup is never retried, leaving the shell without its config | low | fixed | [fe-core] frontend/src/App.tsx: a failed /api/config is now retried with exponential backoff (1s, 2s, 4s… capped at 30s). A stale reply is ignored after authKey/configKey changes or unmount. The sign-in gate shows <Loading /> until the config arrives, so a fresh install no longer shows SignIn in place of CreateAdmin. Test in core.test.tsx renders <App/> with the first /api/config failing and expects 'Create the admin account'. |
| 59 | `fe-core-5` Auto-reload onto a new build discards unsaved non-text edits and aborts in-flight actions | low | fixed | [fe-core] frontend/src/freshness.ts: checkbox, radio and other `change` events now count as edits, and so do pointerdown/click on anything inside a <form>, which covers Seg/Select/TimePicker drafts in forms. client.ts tracks in-flight non-GET requests through a new export, writesPending(). No auto-reload happens on a hidden or re-shown tab, or on a page change, while a write is pending; that covers drag-reorder saves and SSH reinstall/repair. A new useUnsaved(dirty) hook lets panels with drafts outside a form block the reload. Wiring it into those panels is in needs_followup. Two tests in core… |
| 60 | `fe-core-6` scopeCSS can be escaped with an unbalanced '}' or hoisted at-rules, so preview CSS styles the whole dashboard | low | fixed | [fe-core] frontend/src/customCode.ts: rewrote the scopeCSS tokenizer as topLevel(). A '}' that closes nothing is dropped together with the text before it. Backslash escapes, unquoted url(...) and strings cut off at end of line are tokenized the way the browser reads them. Comments become a space, and an unfinished comment is removed. Rules left open at EOF are closed. In the preview, @import, @property and @font-feature-values are dropped because they would reach the whole dashboard; the public page is unaffected. @keyframes and @font-face names get an 'nj-preview-' prefix, and references in a… |
| 61 | `fe-pages-admin-10` One-click Restart for any systemd unit, including the monitor itself, with no confirmation | low | fixed | [fe-admin] frontend/src/pages/SystemServices.tsx: Restart on a running unit asks for an inline confirmation (Restart / Keep running) for the monitor's own unit and for critical units (ssh, sshd, docker, containerd, networking, NetworkManager, systemd-*, dbus, dbus-broker). Other units and Start work as before. A confirmed self-restart treats a dropped connection or timeout as 'restarting', shows an info toast and calls waitForRestart(). A real server refusal (ApiError with a reply, or 401) is shown as an error. Review follow-up: frontend/src/pages/SystemServices.tsx: added selfRestartCut(messa… |
| 62 | `fe-pages-admin-3` Cleanup page freezes on the snapshot from 'Check now' or 'Keep' and ignores later polls | low | fixed | [fe-admin] frontend/src/pages/Cleanup.tsx: useEffect(() => setView(undefined), [data]). The Check now / Keep reply is shown only until the next poll (or the refresh after a failed scan) arrives, then the page follows polled data again. No extra refresh() after a mutation, because that would break the existing media-cleanup test in App.test.tsx, whose mock GET does not reflect the keep. Remaining edge case: a poll already in flight during a mutation can show pre-mutation data for up to one 60 s interval. |
| 63 | `fe-pages-admin-4` Update-all progress compares the browser clock with server run timestamps | low | fixed | [fe-admin] frontend/src/pages/Updates.tsx: a Batch now records `before`, each target's newest run.at (server clock) when Update all starts. itemState(t, since) counts a run as this batch's when run.at > since, so only server timestamps are compared. A batch saved in localStorage without `before` falls back to started-5 as before. The signature is unchanged and the existing itemState tests still pass. No backend change needed. |
| 64 | `fe-pages-admin-6` Notification inbox 'Show older' ignores filter changes and swallows errors | low | fixed | [fe-admin] frontend/src/pages/Notifications.tsx: more() drops its reply if the filters changed (a ref holds the current filters) or the list's last id is no longer the cursor it fetched from. Its errors now show a toast ('Could not load older notifications') instead of becoming an unhandled rejection. The reload on a new `latest` keeps already-loaded older items only when they overlap the fresh page (old[0].id >= oldest fresh id). Otherwise the list restarts from the fresh page, so 'Show older' fills the gap. |
| 65 | `fe-pages-admin-7` SSH login fields overwrite the user's edits with a stale `login` after async calls | low | fixed | [fe-admin] frontend/src/pages/Servers.tsx SSHLoginFields: onChange now takes a functional updater, which both callers already provide via setLogin. set() and the mount effect build on the latest login (`onChange(l => ({...l, ...patch}))`, and the key is filled only if still empty), so User/Port typed while keys load or while Make/Add key runs are kept. |
| 66 | `fe-pages-admin-8` Setup checks step keeps already-added targets in `picked`, so the button count is wrong | low | fixed | [fe-admin] frontend/src/pages/Setup.tsx ChecksStep: `chosen` (suggestions that are still listed and ticked) is computed at render time and drives the button label, the disabled state and add(), so targets added earlier and left in `picked` no longer count. |
| 67 | `fe-pages-admin-9` Agent token edit field is a plain text input | low | fixed | [fe-admin] frontend/src/pages/Servers.tsx EditServer 'New token' input is now type="password" autoComplete="new-password". In the Add-server sheet, the generated token input gets autoComplete="off". It stays visible on purpose, because it is also printed in the install command shown beside it. |
| 68 | `fe-pages-views-10` TimePicker panel can't be closed with Escape while focus is on the field | low | fixed | [fe-views] frontend/src/components/TimePicker.tsx: the trigger's onKey closes an open panel on Escape (preventDefault + stopPropagation, so an enclosing form is unaffected). A root onBlur closes the panel when focus moves to an element outside the picker. A null relatedTarget is ignored so Safari's unfocused button clicks still work, and the existing mousedown handler covers that case. I deliberately did not add automatic focus-into-panel on open, because it would change mouse behaviour. |
| 69 | `fe-pages-views-11` Finder 'Add an uptime check' / 'Add a torrent' do nothing when already on that page | low | fixed | [fe-views] frontend/src/pages/Services.tsx: an effect keyed on [location.hash, location.key] opens the add form, clearing any edit, whenever #add is followed, including when already on /services. frontend/src/pages/QBittorrent.tsx: an effect keyed on [?add=1, location.key] does the same with useLocation. The useState initialisers are kept, so a fresh load doesn't flash. |
| 70 | `fe-pages-views-2` Firewall manager crashes when status.listening is null (not normalised server-side) | low | fixed | [fe-views] In frontend/src/components/Firewall.tsx I added a module-level withLists() that turns a null/missing `listening` into []. Every status that reaches state goes through it: the load in refresh, run() and submit(). presets(), `closing`, the OpenPorts guard and the OpenPorts list can no longer dereference null. The backend normalisation in remote.firewallCall and Manager.Status, and making the TS type optional, are outside my paths (see needs_followup). |
| 71 | `fe-pages-views-3` Select crashes on Enter when its options shrink while open (or when empty) | low | fixed | [fe-views] frontend/src/components/Select.tsx: pick() now bounds-checks (`const o = options[i]; if (!o) return`). A new effect clamps `active` to options.length-1 while the menu is open and closes the menu when the options become empty. show() no longer opens with no options. |
| 72 | `fe-pages-views-4` Select shows the first option as chosen when value is not among the options | low | fixed | [fe-views] frontend/src/components/Select.tsx: `selected` is now the raw findIndex and may be -1. The trigger falls back to showing `value`, and aria-selected, the selected class and the check mark apply only on a real match. show(), ArrowUp/ArrowDown and type-ahead treat -1 as 'start from the top'. frontend/src/pages/Network.tsx: the Interface menu now includes the interface being watched even when it is a down virtual one picked from its card, so the Select shows it correctly. The Firewall page's stale ?server= case lives in pages/Firewall.tsx, which I don't own. It is covered by the Select … |
| 73 | `fe-pages-views-5` Activity log hides or duplicates entries after 'Older' once new activity arrives | low | fixed | [fe-views] frontend/src/components/ActivityPanel.tsx: the `older` array is replaced by `kept`, which accumulates every entry shown once 'Older' has been used. The polled newest page is merged in on every poll, and display is a merge by id sorted by id desc. Entries that slide off the newest page stay visible, and ids can't repeat (no duplicate keys). Behaviour before 'Older' is clicked is unchanged. |
| 74 | `fe-pages-views-6` Clicking a check name pushes two identical history entries | low | fixed | [fe-views] frontend/src/pages/Services.tsx (ServiceRow) and frontend/src/pages/Overview.tsx (services table): the row's onClick now ignores clicks inside an <a> (`!(e.target as Element).closest('a') && navigate(href)`). A click on the name is left to the Link and pushes once, and Ctrl/Cmd-click no longer also navigates the current tab. |
| 75 | `fe-pages-views-8` Malformed URL hash crashes the Docs page (decodeURIComponent in render) | low | fixed | [fe-views] frontend/src/pages/Docs.tsx: added a hashId() helper that decodeURIComponent()s the fragment inside try/catch and falls back to the raw text. Both call sites (the scroll effect and `current` during render) use it. |
| 76 | `fe-pages-views-9` Firewall form sends a stale protocol after the port is cleared, and the protocol control is disabled | low | fixed | [fe-views] frontend/src/components/Firewall.tsx: submit sends `proto: (draft.port.trim() && draft.proto) \|\| undefined`, so the protocol is only sent with a port. The disabled protocol <select> now shows 'tcp and udp' while the port is empty, not a stale 'tcp'. The remembered choice comes back if a port is typed again. |
| 77 | `gap-2` Status page designs within the per-field limits are rejected by the 64 KiB body cap | low | fixed | [api] internal/api/writes.go adds writableUpTo/fromDashboardUpTo. PUT /api/statuspage now uses a 2 MiB cap (maxDesignBody in statuspage.go) instead of 64 KiB. When the cap trips, saveStatusPageDesign returns 413 with a message saying the design is too large, instead of 'Expected a status page design'. |
| 78 | `gap-3` App-store reinstall runs setup as root inside the app user's data directory and follows that user's symlinks | low | fixed | [updates] backend/internal/apps/script.go: the chown -R of the app's dir and data dir now runs before setup, and the setup snippet runs as the app's user via `runuser -u <user> -- bash -euo pipefail <<'SETUP'`, so a symlink the app user plants can only lead where that user could already write. backend/internal/apps/catalog.go: added a `prepare` field (shell run as root before setup). Gitea's apt-get install of git moved into it; its mkdir stays in setup. Every other setup snippet works as the app user. Covered by TestInstallKeepsWhatIsNotTheStoresAndSetsUpAsTheAppsUser; TestEveryScriptIsValidB… |
| 79 | `gap-4` Session revocation can be undone: a concurrent Valid() writes the deleted session back to SQLite | low | fixed | [api] internal/accounts/accounts.go Valid: the hourly 'last seen' write (SaveSession) now happens while holding a.mu, so EndSession or ChangePassword cannot delete the session in between and have the upsert put it back. A new pruneLocked removes sessions unused for longer than SessionLife, from memory and SQLite, at New() and on every NewSession. |
| 80 | `gap-5` Incident feedback and notices: unlocked read-modify-write loses updates, and a failed meta read wipes every entry | low | fixed (finished in the cross-area pass) | [notify] backend/internal/incidents/feedback.go: keep() is now serialized with a package-level mutex, so concurrent SetNotice/SetFeedback calls no longer overwrite each other. load() now returns an error when stored JSON cannot be parsed, and keep() then refuses to write instead of wiping every other entry. json.Marshal errors are no longer ignored. Tests: TestSavingOneEntryNeverLosesTheOthers (20 concurrent saves, plus unreadable stored data left untouched). Still open: when the SQLite read itself fails, storage.Store.Meta returns "" and swallows the error, which looks the same as having noth… |
| 81 | `gap-6` Firewall 'limit' rules on IPv6 count per /128, so one client with a /64 bypasses them | low | fixed | [ddos] firewall/build.go: lines() and limitMatch() now take the family. IPv6 limit rules get --hashlimit-srcmask 64, so one /64 shares a single bucket. IPv4 output is unchanged. Test: TestLimitsCountAnIPv6SlashSixtyFourAsOne. |
| 82 | `host-collect-13` Kernel crash alerts from syslog files can be spoofed by any local user | low | fixed | [host] backend/internal/kernel/monitor.go: kernelLogCrashes now reads only /var/log/kern.log and no longer reads /var/log/syslog or /var/log/messages, where any local user can write a line tagged 'kernel' with `logger -t kernel`. The journal check (_TRANSPORT=kernel) and dmesg still cover kernel crashes on RHEL-style hosts that have no kern.log. |
| 83 | `host-collect-14` dmesg crash check filters to err,warn levels and misses Oops/BUG/panic messages, which the kernel logs at alert/emerg | low | fixed | [host] backend/internal/kernel/monitor.go dmesgCrashes: now runs dmesg with --level=emerg,alert,crit,err,warn, so panics, oopses and BUGs logged at emerg, alert or crit are included. |
| 84 | `host-collect-15` DiskIO sums whole disks, partitions and dm devices, double-counting throughput | low | fixed | [host] backend/internal/collect/host.go DiskIO: sums only whole hardware disks, meaning entries with /sys/block/<name>/device (sda, nvme0n1, vda, mmcblk0). Partitions, dm-*, md*, loop and zram are left out because they pass their I/O on to a disk. If no entry qualifies (for example on non-Linux) it falls back to summing every entry. |
| 85 | `host-collect-16` Partitions are deduplicated by mountpoint, not device, so btrfs subvolumes and bind mounts are counted twice in TotalDisk | low | fixed | [host] backend/internal/collect/host.go Partitions: deduplicates by filesystem as well as by mountpoint. The key is the block device for /dev/* devices and the mountpoint otherwise (tmpfs, network shares, ZFS datasets). btrfs subvolumes and bind mounts of the same device are listed once, at their first monitorable mount, and are no longer counted twice in TotalDisk. Smoke test: TestAFilesystemIsListedOnce. |
| 86 | `host-collect-17` Drives behind RAID controllers (megaraid/cciss) share one map key and overwrite each other | low | fixed | [host] backend/internal/smart/smart.go: Reader now keys drives, order, the previous-reading lookup in readOne, and Restore by Device+'\|'+Type. Disks behind a MegaRAID or cciss controller share the name /dev/bus/N and differ only in type, so they no longer overwrite each other. Test: TestDrivesBehindARaidControllerAreKeptApart. |
| 87 | `host-collect-18` Forced journal vacuum is less aggressive than the normal one, and a small threshold vacuums to 0M | low | fixed | [host] backend/internal/cleanup/logs.go vacuumJournal: a forced vacuum now uses min(threshold, 100M) instead of a fixed 100M, so it is never gentler than a routine pass. The size is computed in whole MB and clamped to at least 1M, so it can never become '0M'. Not unit-tested, because the function runs journalctl. |
| 88 | `host-collect-19` Stability score counts OOM kills since boot, so one old OOM inflates the risk until reboot | low | fixed | [host] backend/internal/kernel/monitor.go: tracking has a new persisted LastOOMKills (*int64, json last_oom_kills). CheckStability adds the +25 OOM score only for kills since the previous check. The first check after an upgrade or a fresh install takes the current count as its baseline. A reboot, detected either at load or at runtime, resets the baseline to 0, because the counter restarts at boot. oomKills now returns (int64, ok). |
| 89 | `host-collect-20` Process handles cached by PID keep the old process's name after PID reuse | low | fixed | [host] backend/internal/collect/processes.go: when a cached handle is reused, it is checked with gopsutil IsRunning(), which compares its cached create time with a fresh read. If the PID now belongs to a new process, the handle is replaced. New handles read CreateTime immediately so later scans have a value to compare against. This costs one extra /proc/<pid>/stat read per process per scan. |
| 90 | `host-collect-7` Journal reads keep only the oldest 2000 entries and advance lastJournal even on timeout, so newer kernel errors are permanently skipped | low | fixed | [host] backend/internal/kernel/monitor.go: journalEntries now runs journalctl with --reverse, so the 2000-entry cap drops the oldest entries, not the newest. It also returns whether the read completed (false on a start failure or a 15s timeout). CheckCrashes advances lastJournal only when both journal reads completed, and sets it to the time the reads started, so a timed-out span is read again next cycle. Entries already read are deduplicated by seen. |
| 91 | `integrations-10` qBittorrent client builds an invalid URL for IPv6 hosts without a port | low | fixed | [integrations] qbittorrent/client.go New: u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(port)), so IPv6 hosts keep their brackets (http://[::1] -> http://[::1]:8080). Test: TestNewIPv6HostGetsPort covers a URL, a bare [fd00::5], and an explicit port that still wins. |
| 92 | `integrations-4` Heartbeat secret token (and any credentials in check URLs) are posted in every Discord alert | low | fixed | [monitor] internal/monitor/services.go: new alertTarget(spec). Heartbeat checks get no Target field, since the token is the only authentication on /ping. For URLs it removes the user and password and replaces the values of credential-like query parameters (key, token, secret, pass, auth, sig, ...) with REDACTED. A target that is not a well-formed URL has any 'user:password@' removed by withoutPassword. Test: TestAlertsDoNotCarryCheckSecrets. |
| 93 | `integrations-6` 8 MB response cap makes media cleanup fail on large Plex libraries | low | fixed | [integrations] plex/client.go: get() now reads maxBody+1 bytes (8 MB) and returns a clear "Plex's answer to <path> is larger than the 8 MB Nightjar reads" error rather than passing truncated JSON to the parser. Items() now pages /library/sections/X/all with X-Plex-Container-Start/Size, 500 per page, de-duplicates by ratingKey, and returns "<library> changed while it was read" when totalSize changes between pages or fewer items than totalSize were read. A shifted page therefore cannot silently drop a listed item and reset its notice. Tests: TestItemsArePaged and TestOversizedAnswer. The existin… |
| 94 | `integrations-7` Items in Failed status stay due while cleanup is off and are deleted with no new notice when it is turned back on | low | fixed | [integrations] mediacleanup.go: the reset when cleanup is turned off now includes Failed (Leaving/Waiting/Failed -> Preview, LeavingAt=0, Reason=""). Turning cleanup back on therefore re-lists the item with a fresh notice instead of removing it in the first pass. Test: TestFailedStartsOverWhenTurnedOff. |
| 95 | `integrations-8` A heartbeat that has never been pinged loses its listening start on every restart and may never go late | low | fixed | [integrations] backend/internal/services/heartbeat.go pings(): when the store has no record for a token, the new record (with Seen=now) is saved straight away. A heartbeat that has never pinged therefore keeps its original listening start across restarts, and the every/cron missed-run logic can mark it late. Test: TestHeartbeatNeverPingedAcrossRestarts in backend/test/services/heartbeat_test.go (three restarts, late a day after it was added). Review follow-up: backend/internal/services/heartbeat.go pings(): now reads with store.MetaValue and unmarshals when the value is non-empty. It writes th… |
| 96 | `integrations-9` Inactive-torrent auto-remove deletes torrents the dashboard just added in the stopped state, and paused partial downloads | low | fixed | [integrations] backend/internal/qbittorrent/client.go RemoveReason now returns "inactive" only for finished torrents (InactiveStates and Progress >= 1). Stopped or paused downloads, including ones the dashboard added stopped, are no longer auto-removed and keep their progress and partial files. Test: TestInactiveMeansFinished. The setting's label and docs still say "paused torrents"; see needs_followup. |
| 97 | `monitor-core-10` Aggressive log check never re-trims a log it already cleaned while the disk stays under pressure | low | fixed | [monitor] internal/monitor/logs.go and loop.go: cleaned is now map[string]int64, holding each log's size just after it was trimmed; for the journal that size comes from JournalSize. A cleaned log is skipped only while its size is at or below that value. Once it regrows it is trimmed again, still limited by the per-path lastAlert cooldown. No regression test: the path calls Cleaner.RestartLogServices, which would restart real services on this live host. Review follow-up: In backend/internal/monitor/logs.go, `cleanOne` now applies the regrow rule only in aggressive mode: `done && (!aggressive \|… |
| 98 | `monitor-core-11` Weekly maintenance windows shift by an hour on DST transition days | low | fixed | [monitor] internal/maintenance/maintenance.go: Window.At builds start and end with time.Date from the wall-clock hour and minute, with end on the next day when To <= From. It no longer adds minutes to midnight as a fixed duration, so DST days keep the times set. Rename now replaces a window's Checks slice with an edited clone, so copies already returned by List or For are not changed. Tests: TestWeeklyWindowKeepsItsWallClockOnDaylightSavingDays (America/New_York, both DST changes), TestRenameLeavesCopiesAlone. |
| 99 | `monitor-core-3` Hung network mount leaks one blocked goroutine (and OS thread) per cycle and silently disables disk monitoring | low | fixed | [monitor] The verifier showed the main claim was wrong: gopsutil already skips nodev network mounts. I fixed what was left. internal/monitor/resources.go: mediaCheck now runs os.Stat and DirSize off the loop goroutine, inside withTimeout (90s budget). An atomic `sizing` flag stops a second sizing from starting while one is stuck on a dead mount. A partial DirSize is logged as 'at least X'. collectCore's partition read has a matching `partitioning` flag, so reads stuck on a hung mount cannot pile up one goroutine per cycle; while one is stuck, the cycle logs a warning and records a disk_monitor… |
| 100 | `monitor-core-6` Fixed gate recovery window shorter than a check's cadence resets every episode, disabling cooldown/backoff for slow checks | low | fixed | [monitor] internal/alerts/gate.go: each Gate now has a per-key floor. Gates.GetEvery(key, now, interval) keeps an episode going for at least 3 intervals, and Gates.SetRecovery updates every gate. internal/monitor/context.go: new raiseAlertEvery. applyPending calls Gates.SetRecovery(GateRecovery(cfg)), where GateRecovery = max(ALERT_RECOVERY_SECONDS, 3*CHECK_INTERVAL), so a live CHECK_INTERVAL change takes effect. These now pass their own interval: uptime down alerts (spec.Interval, services.go), drive temperature (SmartCheckInterval, captured before the goroutine in system.go), the media alert… |
| 101 | `monitor-core-7` alerts.Gate is mutated without synchronization from multiple goroutines | low | fixed | [monitor] internal/alerts/gate.go: Gate has its own mutex, and every method locks it. New Gate.Allow(now) does ShouldAlert and MarkSent atomically. Prune reads each gate through g.idle() under that gate's lock. raiseAlert, diskAlerts.gateOK and earlyWarning now call Allow before sending, so two reports of the same condition at once cannot both alert. MarkSent was already recorded even when a send failed, so moving it before the send does not change behaviour. Tests: TestAllowLetsOneOfManyThrough, TestPruneIsSafeWhileGatesAreInUse (pass under -race). |
| 102 | `monitor-core-8` Emergency 'cleanup freed nothing' Critical alert is ungated and repeats every 5 minutes | low | fixed | [monitor] internal/monitor/disk.go: new handler.notify sends cleanup notifications through raiseAlertEvery, with a key per mount and the cleanup cadence as the interval. diskCleanup emergency ('freed nothing' and 'was N% full') uses cleanup-emergency:/; routine uses cleanup:/. partitionMaintenance emergency and routine use cleanup-emergency:<mnt> and cleanup:<mnt>. All of them now back off up to ALERT_MAX_COOLDOWN instead of repeating every 5 minutes. |
| 103 | `monitor-core-9` Duplicate early-warning alerts on NVMe partitions, and MONITOR_INDIVIDUAL_PARTITIONS=false does not switch to total-only alerts | low | fixed | [monitor] internal/monitor/disk.go: removed the separate NVMe early-warning pass, and the dead thresholdAlert that shared the partition:<mnt> key. maintain() now sends the early warning only between the early and main thresholds; above the main threshold alertPartitions has already alerted. alertPartitions returns when MonitorIndividualPartitions is false, and alertTotal then alerts on the total. Tests: TestNVMeEarlyWarningIsSentOnce, TestTotalOnlyWhenPartitionsAreNotWatchedOneByOne. |
| 104 | `notify-incidents-10` Weekly report is marked sent before delivery, so a transient failure loses that week's report | low | fixed | [notify] backend/internal/report/report.go Schedule: sentKey is now written only after Send succeeds. A failed send logs a warning and is retried every 30 minutes (retryEvery) while still inside the existing 24h window, so nothing is retried every minute and nothing is sent twice. No unit test was added: Schedule runs on a real 1-minute ticker and wall clock, and testing it would mean restructuring it. |
| 105 | `notify-incidents-4` Plugin notify channel has no circuit breaker; a hung plugin stalls the synchronous monitor loop up to 2 minutes per alert | low | fixed | [notify] backend/internal/notify/channels.go: plugin channels now have a CircuitBreaker (5 failures, 1 minute, 2 successes, like the other channels). SendEmbed waits at most PluginWait (exported var, default 20s, the default plugin timeout) and then gives up, so a hung plugin cannot block the synchronous monitor loop for the full 2-minute context. The plugin process still runs to its own timeout in the background. Test TestAHungPluginDoesNotHoldUpAlerts checks that the wait is bounded and that the breaker opens after 5 failures. |
| 106 | `notify-incidents-5` Telegram message truncated on the HTML string can cut tags/entities, making Telegram reject the alert | low | fixed | [notify] channels.go: new telegramText() budgets Telegram's 4096-character limit against the raw text, cutting the title, description and field values before they are HTML-escaped and marked up. Fields that do not fit are dropped and the footer is always kept, so the HTML stays well-formed. The discord.Truncate on the finished HTML is gone. Test TestLongTelegramMessagesStayWellFormed checks balanced tags, no cut entities, visible length <= 4096 and that the footer survives. |
| 107 | `notify-incidents-6` Slack payload does not escape <, >, & so untrusted alert text can trigger @channel mentions or spoofed links | low | fixed | [notify] channels.go NewSlack: &, < and > are escaped to &amp;/&lt;/&gt; in the top-level text, attachment title, text, field titles and values, and the footer, before the **bold** conversion runs. Test TestSlackTextCannotMentionOrLink. |
| 108 | `notify-incidents-7` Email HTML part is emitted as one line far over the 998-octet SMTP limit | low | fixed | [notify] channels.go email.message: both the text/plain and text/html parts now carry Content-Transfer-Encoding: quoted-printable and are written through mime/quotedprintable, which keeps lines at 76 characters or fewer. TestEmail now decodes the multipart message and asserts that no line is over 998 octets. Added TestLongEmailsKeepShortLines. The fake SMTP server in the test now undoes dot-stuffing. |
| 109 | `notify-incidents-8` Classic embed fallback ignores Discord's 6000-character total embed limit | low | fixed | [notify] backend/internal/discord/client.go: BuildEmbed now calls a new fit() that enforces Discord's 6000-character total across title, description, field names and values, and footer. The description shrinks first (down to 1024), then trailing fields are dropped, then the description shrinks further. TestBuildEmbedLimits was adjusted, since its 25 maximal fields can no longer all fit, and TestBuildEmbedKeepsWithinTheTotal was added. |
| 110 | `remote-agent-3` Agent server WriteTimeout (10s) is shorter than its firewall operations (iptables -w calls up to 20s each) | low | fixed | [remote] backend/internal/agent/firewall.go: the guard every firewall, defense and blocked route passes through now extends the write deadline to 45s (firewallWait), using http.NewResponseController(w).SetWriteDeadline, as updateHandler already does. That is longer than the dashboard's 30s client limit. The 10s server WriteTimeout is unchanged for the other routes. No regression test: it would need an iptables call slower than 10s. |
| 111 | `remote-agent-4` Auto-update kills an agent that is not run under systemd | low | fixed | [remote] backend/internal/agent/update.go: the default restart now checks for systemd supervision (INVOCATION_ID). Under systemd it exits as before (Restart=always starts the new build). Otherwise it runs syscall.Exec on the new build in the same process, so an agent started by hand under tmux or nohup keeps running on the new build. If the exec fails, it logs and keeps serving. CanUpdate is unchanged, so foreground agents still receive updates. No automated test, since the path exits or execs the process. |
| 112 | `remote-agent-5` Concurrent update requests race on the same .new file and on Self.Hash | low | fixed | [remote] backend/internal/agent/update.go: Self has an unexported mutex. Apply holds it for the hash compare, the write and the Hash update, so concurrent pushes run one at a time and the second becomes a no-op. The build is written to a unique os.CreateTemp file beside the program, chmodded to 0755, fsynced, closed and then renamed; the temp file is removed on any failure. A new Self.Build() reads Hash under the lock, and the readings handler in agent.go uses it instead of reading self.Hash directly, which fixes the data race. Regression test TestUpdatesArriveOneAtATimeAsAWholeBuild (passes u… |
| 113 | `remote-agent-6` Pinned HTTPS clients for failed Add/Update tokens are cached forever | low | fixed | [remote] backend/internal/remote/remote.go: added forgetUnusedClient / forgetUnusedClientLocked, which drop the cached pinned client for a token that no saved host uses. Called when Add's fetch or validName fails, when Update's verification fetch fails, and when the host disappears during Update. The lock order (m.mu, then clientsMu) matches the existing forgetClient call in Update. No black-box test: m.clients is unexported and nothing exported shows it. |
| 114 | `remote-agent-7` Authorize-with-password trusts an unseen host key and sends the account password on first contact | low | fixed | [remote] Backend fixed in backend/internal/remote/ssh.go; the dashboard flow needs the api and frontend follow-ups listed. AuthorizeKey now sends the password only to a server whose host key is already in known_hosts, and logs in with StrictHostKeyChecking=yes (sshArgs now takes the checking mode; key logins still use accept-new). For an unseen server it runs ssh-keyscan plus `ssh-keygen -l -f -` without logging in, and returns a new *remote.HostKeyError{HostKey: "ED25519 SHA256:…"} whose message says how to check the fingerprint on the server. New TrustHostKey(ctx, id, login, hostKey) re-scan… |
| 115 | `storage-config-main-10` env.recommended documents DISK_FULL_THRESHOLD, a safeguard that does not exist | low | fixed | [storage] Removed the DISK_FULL_THRESHOLD block from env.recommended. Nothing reads it, and README has no mention of it. |
| 116 | `storage-config-main-11` Analytics "peak" memory and latency are maxima of bucket averages, not real peaks | low | fixed | [storage] The samples table gains mem_max and latency_max columns, added to existing databases through a small idempotent migrate() in storage.Open that checks pragma_table_info before ALTER TABLE. Sample has MemMax/LatencyMax (json mem_max/latency_max, new fields only). AddSamples stores them, and SampleBuckets returns MAX(COALESCE(mem_max, mem)) and MAX(COALESCE(latency_max, latency)), so older rows fall back to their average. analytics Recorder keeps per-minute memMax/latencyMax, and summarize takes MemPeak/LatencyPeak from them. Tests TestPeaksAreKeptPerMinute (including an old-schema DB) … |
| 117 | `storage-config-main-12` During shutdown, further signals are swallowed and background writers are not awaited before db.Close | low | fixed (finished in the cross-area pass) | [storage] backend/cmd/nightjar/main.go: (1) The restart goroutine sets restart only if stop has not already been cancelled by a signal, so a signal arriving with a restart request wins. It also closes a 'decided' channel that is awaited before restart is read, which removes the data race. (2) After loop.Run returns, cancel() unregisters the signal handlers, so a second Ctrl+C or SIGTERM during a slow shutdown kills the process. (3) Plugins.Loop, Attacks.Run, Firewall.Start, WatchCaps, report.Schedule and MediaCleanup.Loop now run in a sync.WaitGroup, which is joined for up to 5s before Analyti… |
| 118 | `storage-config-main-13` LoadDotenv puts every secret into the process environment, and repair and app scripts inherit it | low | fixed (finished in the cross-area pass) | [storage] backend/internal/config/env.go: added config.Environ(), which returns os.Environ() minus every KindSecret setting key (webhooks, tokens, passwords, API keys). Test TestEnvironLeavesOutSecrets added. I left LoadDotenv as it is: removing secrets from the process environment would break api/settings.go, which reads them via os.LookupEnv, and the systemd EnvironmentFile puts them in the environment anyway. The real fix is for the child-process spawn sites, which are outside my paths, to use config.Environ() (see needs_followup). |
| 119 | `storage-config-main-2` Number settings accept NaN/Inf; CHECK_INTERVAL=NaN makes the monitoring loop spin without sleeping | low | fixed | [storage] backend/internal/config/fields.go number(): values where math.IsNaN or math.IsInf is true are rejected with the usual "%q is not a number" error before the bounds check. LoadFrom falls back to the default and Validate reports the key. Tests added in backend/test/config/config_test.go (LoadFrom with NaN/Inf/infinity variants; Validate rejecting CHECK_INTERVAL=NaN and ALERT_COOLDOWN=+Inf). |
| 120 | `storage-config-main-7` Plugin ownership and permission checks run only at Scan, skip intermediate directories, and are not repeated before the plugin runs as root | low | fixed | [storage] backend/internal/plugins/plugins.go: new Plugin.runnable() checks owner and go-w with owned() on the plugins folder, the resolved plugin folder, every directory between the plugin folder and the program (both the path as written in run and the resolved path), and the program itself. problem() uses it, and still checks plugin.json. Manager.run() now calls runnable() instead of program() right before every exec, so a bin/ folder or plugins folder opened up after the scan is refused without a rescan. Ancestors above the plugins folder are left to selfcheck.Exposed, which already warns a… |
| 121 | `storage-config-main-9` repairs table is never pruned (PruneRepairs has no caller) | low | fixed | [storage] The suggested caller, monitor/services.go, is outside my paths, so I fixed it in storage. backend/internal/storage/store.go Store.Prune, which the services monitor already calls with the HISTORY_RETENTION_DAYS cutoff, now also calls PruneRepairs with the same cutoff, so repairs follow history retention. Test TestPruneDropsOldRepairsToo added. |
| 122 | `updates-apps-10` Poll/Refresh race produces a spurious 'service failed' alert right after a dashboard restart | low | fixed | [updates] backend/internal/units/units.go: added a Refresh counter (reads) and a per-unit refreshed[name] stamp. Poll records the counter before reading systemd and skips any unit refreshed after that point: no flapping or transition events, and m.units is not overwritten with the older reading. forgetGone clears the stamp. Regression test: TestARestartDuringAPollRaisesNoAlerts in backend/test/units/units_test.go. |
| 123 | `updates-apps-11` App install wipes an existing /opt/<id> that the 'installed elsewhere' check missed | low | fixed | [updates] backend/internal/apps/script.go installScript: the store now records that it owns /opt/<id> and /var/lib/<id> with a root-owned marker, ManifestDir/<id>.owned, written before anything there changes. If neither the manifest nor the marker exists, an existing /opt/<id> or /var/lib/<id> (including a symlink) is moved to <path>.before-nightjar-<ts> with a log line, so it is no longer deleted or chowned. Retrying a failed store install still reuses its own directories. removeScript keeps the marker on a plain remove, so kept settings are reused on reinstall, and deletes it on purge. Cover… |
| 124 | `updates-apps-6` Git targets silently run as root (with safe.directory=*) when the checkout owner can't be resolved | low | fixed | [updates] backend/internal/updates/kinds.go: owner() now returns (string, error). If the checkout's uid has no passwd entry and is not the monitor's own euid, it returns an error ('belongs to uid N, which has no account to run git as; give it to a user with chown') and no longer falls back to running as root. gitUser propagates the error, and git() and installGit() return it before anything runs. A Stat failure still leaves the error to git, as before. Regression test: TestGitCheckoutOfAnUnknownUidIsRefused (runs as root and chowns a temp dir to an unused uid). |
| 125 | `updates-apps-7` Check round is queued twice per interval because Tick ignores an in-progress round | low | fixed | [updates] backend/internal/updates/manager.go Tick: no longer queues a 'check' round while m.checking is true, so a round longer than one cycle is not queued again behind itself. Regression test: TestALongCheckRoundIsNotQueuedTwice. |
| 126 | `updates-apps-8` Root update commands lose $TAG/$VERSION/DEBIAN_FRONTEND when run through sudo (env_reset) | low | fixed | [updates] backend/internal/updates/exec.go: when a command goes through `sudo -n [-u user] --`, its extra variables (c.Env: DEBIAN_FRONTEND, VERSION/TAG, GIT_TERMINAL_PROMPT...) are passed as `env K=V... argv`, so sudo's env_reset no longer drops them. No automated test: the sudo path only runs when the monitor is not root, and the test host runs as root. |
| 127 | `updates-apps-9` *arr targets read from config.xml always use 127.0.0.1, ignoring a configured BindAddress | low | fixed | [updates] backend/internal/updates/kinds.go arrEndpoint: uses BindAddress when it is a concrete IP (not '*', empty or unspecified like 0.0.0.0/::), via net.JoinHostPort so IPv6 is bracketed, and otherwise uses 127.0.0.1. Regression test: TestArrBoundToOneAddress (the server listens on 127.0.0.2 only). |
| 128 | `x-contract-5` Analytics 'points' is null when the database is unavailable, so the Analytics page and server detail chart crash | low | fixed (finished in the cross-area pass) | [storage] backend/internal/storage/samples.go SampleBuckets now returns an empty []Sample{} on every early exit (width<=0, no DB, query error), so analytics.Read and /api/analytics send "points": [] rather than null. Test TestUnavailableStoreSaysSo added. The frontend guard is the fe-views group's part. / [fe-views] Frontend part only (my paths): frontend/src/pages/Analytics.tsx normalises every host's `points` to [] in the hosts memo. The 'database is unavailable' banner now renders, and Tiles, HostsTable and Trends don't throw. The backend fix (SampleBuckets/analytics.Read returning []) belo… |
| 129 | `x-contract-7` Activity log skips any change whose path ends in /test, /try, /detect or /rescan, so actions on a check named 'test' are never recorded | low | fixed | [api] internal/api/activity.go: the skip list is now a set of route patterns matched on r.Pattern (checks/settings/alerts test, plugins try/rescan, updates check/detect, 2fa/begin, login/logout, notifications/read, hosts/token), plus the SSH 'test' action, instead of path suffixes. Changing or deleting a check named 'test', 'try', 'detect' or 'rescan' is now recorded. |
| 130 | `x-security-6` Discord interactions endpoint has no replay protection (timestamp freshness or interaction-id dedupe) | low | fixed | [notify] backend/internal/discord/bot.go Interactions (this also covers notify-incidents-9, which is the same finding): after the signature check, a request whose X-Signature-Timestamp is unparsable or more than 5 minutes from now gets 401. Button interactions (type 3) are deduplicated by their signed body "id" in a short-lived seen map, pruned after 10 minutes. A replay or a missing id gets an ephemeral 'already handled' reply and does not act. TestBot now signs with the current time and checks that a replayed click and a stale click are both refused without acting. |
