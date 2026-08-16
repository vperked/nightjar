"""
Server Monitor subsystems.

Collectors and services are packages that re-export their public API from
__init__; small support helpers are single modules under core/:

    from core.cpu import CpuMonitor
    from core.format import format_bytes

Reach into an implementation module only for internals that are deliberately not
public, such as core.config.settings for the environment cache in tests.

Collectors      cpu, memory, disk, network, gpu, sensors, system, processes,
                freeze, kernel, logs, watchdog, qbittorrent, updates
Orchestration   runtime, selfTest
Services        webhook (Discord), web (dashboard and API), systemd
Support         config, detection, metrics, temperature, cache, format,
                security, circuitBreaker, timeout, observability (logging)
"""
