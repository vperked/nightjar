"""
Server Monitor subsystems.

Each subsystem is a package holding its own implementation module, and re-exports
its public names from __init__ so callers import from the package:

    from core.cpu import CpuMonitor
    from core.disk import DiskMonitor

Reach into an implementation module only for internals that are deliberately not
public, such as core.config.settings for the environment cache in tests.

Collectors      cpu, memory, disk, network, gpu, sensors, system, processes,
                freeze, kernel, logs, watchdog, qbittorrent, updates
Services        webhook (Discord), web (dashboard and API), systemd
Support         config, detection, metrics, alert, temperature, cache, format,
                security, circuitBreaker, errors, observability (logging)
"""
