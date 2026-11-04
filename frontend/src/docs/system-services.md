# System services and drives

## systemd services

**[Services → System services](/system)** lists every systemd service on this machine: running, stopped, failed or restarting, and since when. Open one to read its recent log and to **start**, **stop** or **restart** it. The monitor won't stop itself.

The monitor alerts when a service **fails**, when it keeps **restarting** (3 restarts within ten minutes by default), and when it comes back. The Overview's _Needs attention_ card lists any that are failed or restarting right now.

Settings, under **[Settings → System services](/settings)**:

- `SERVICE_WATCH_IGNORE`: services never to alert on, by name or pattern, such as `apt-daily, certbot*`.
- `SERVICE_RESTART_ALERT`: how many restarts in ten minutes count as a restart loop (0 turns that alert off).

## What went wrong

When a service fails or keeps restarting, the monitor reads its last journal lines and works out why. The cause shows on the service's row, at the top of its log under **What went wrong**, and in the alert, each with what it means and how to fix it.

It recognises the usual causes and picks out the detail that matters:

- **A port already in use**, with the port and the command that shows what holds it.
- **Permission denied**, with the path the service could not open.
- **A full or read-only disk**, **out of memory**, and **too many open files**.
- **A missing program, file, user, folder or mount**, including systemd's own exit codes such as 203 (program missing) and 217 (user missing).
- **A broken settings file**, a **missing Python or Node module**, a **damaged or locked database**, and **crashes**.
- **Network trouble**: connection refused, a host name that does not resolve, no route, a bad or expired certificate, a refused login.

A root cause ranks above its symptoms, so _Port 8989 is already in use_ comes before _systemd stopped retrying_, and is marked **Likely cause**. Errors it does not recognise are still grouped: lines that differ only in numbers, paths, addresses or quoted values count as one, so a message repeated a hundred times reads as one problem with a count.

> [!TIP]
> Open **The log line** under a finding to see exactly what it read.

## Drive health

The Overview's **Drive health** card reads each disk's SMART data every half hour: whether it passes, its temperature, its age, wear on SSDs, and counters that warn of failure, such as reallocated or pending sectors. It needs `smartmontools` installed (`apt install smartmontools`).

Sleeping disks are never woken up just to be read. They show as _asleep_, with when they were last read.

A drive alerts when its health turns to warning or failing, and when it runs hotter than its limit (55 °C for hard disks, 70 °C for SSDs, under **[Settings → Drive health](/settings)**). You can mute a drive from its row.
