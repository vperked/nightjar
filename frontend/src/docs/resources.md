# Resources, disks and the kernel

## Thresholds

Each monitoring cycle (every 5 seconds by default) reads CPU, memory, GPU, temperature, disks, network and processes, and alerts when one crosses its threshold. The thresholds are under **[Settings → Resources](/settings)** and **Disks**:

| Setting                                             | Default   | Alerts when                                             |
| --------------------------------------------------- | --------- | ------------------------------------------------------- |
| `CPU_THRESHOLD`                                     | 80%       | average CPU use reaches it                              |
| `MEMORY_THRESHOLD`                                  | 80%       | memory use reaches it                                   |
| `GPU_THRESHOLD`                                     | 80%       | an NVIDIA GPU's use reaches it                          |
| `TEMP_THRESHOLD`                                    | 80 °C     | the CPU stays this hot for `TEMP_ALERT_DURATION` (30 s) |
| `NETWORK_BANDWIDTH_THRESHOLD`                       | 1000 MB/s | upload plus download reaches it                         |
| `PROCESS_CPU_THRESHOLD`, `PROCESS_MEMORY_THRESHOLD` | 50%, 10%  | one process uses that much                              |
| `DISK_EARLY_WARNING_THRESHOLD`, `DISK_THRESHOLD`    | 75%, 80%  | a partition fills that far                              |

A temperature alert includes a likely cause, such as a busy process or a failing fan. Changes apply from the next cycle, without a restart.

## Repeats and cooldowns

An alert doesn't repeat every cycle. After it fires, the same alert waits `ALERT_COOLDOWN` (5 minutes), and the wait grows each time it repeats, up to `ALERT_MAX_COOLDOWN` (an hour). Once the condition has stayed clear for `ALERT_RECOVERY_SECONDS`, the wait starts over. Conditions that flap on and off are held back.

## Disk cleanup

As a disk fills, the monitor makes room on it. Only what is on that disk counts: the root filesystem's own usage sets off the log and cache cleanup there, and another disk filling up cleans only the temp and cache folders that live on it, which for a data disk is usually nothing.

- At `DISK_CLEANUP_THRESHOLD` (85%) it removes old temp files, apt's caches and rotated logs older than `ROTATED_LOG_RETENTION_DAYS`.
- At `DISK_AGGRESSIVE_CLEANUP_THRESHOLD` (92%) it also empties large logs and package caches.
- At `DISK_EMERGENCY_THRESHOLD` (95%) it does everything it safely can, and says what it freed.

It only touches `/tmp`, `/var/tmp` and `/var/cache` unless you list other paths and allow them with `ALLOW_UNSAFE_CLEANUP_PATHS`. Log files over `LOG_THRESHOLD_MB` are trimmed, and services listed in `LOG_SERVICES_TO_RESTART` are restarted so they reopen their logs.

## The kernel and freezes

With `KERNEL_MONITOR_ENABLED` (on by default) the monitor reads the kernel log for panics, oopses, hardware errors, memory pressure and unexpected reboots, and keeps a stability score. It needs to read `journalctl`, `dmesg` or `/var/log/kern.log`, which running as root allows.

The **watchdog** alerts if a monitoring cycle stalls for `MONITORING_WATCHDOG_TIMEOUT` (30 s), and with `WATCHDOG_AUTO_RESTART` restarts the monitor's service. `FREEZE_MONITOR_ENABLED` also detects the whole machine freezing.
