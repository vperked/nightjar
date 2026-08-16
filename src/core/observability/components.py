"""
Canonical component names for logging.

Every module logs through one of these names so log output is filterable and
consistent. Names are short PascalCase nouns without a 'Monitor' suffix, and
appear in log lines as 'ServerMonitor.<Component>'.

Add a new constant here before using it; the test suite asserts that no module
invents an unregistered component name.
"""

ROOT = 'ServerMonitor'


class Component:
    """Registered logger component names."""

    CORE = 'Core'
    CONFIG = 'Config'
    METRICS = 'Metrics'
    SYSTEM_INFO = 'SystemInfo'
    TEMPERATURE = 'Temperature'

    CPU = 'Cpu'
    MEMORY = 'Memory'
    DISK = 'Disk'
    NETWORK = 'Network'
    GPU = 'Gpu'
    SENSORS = 'Sensors'
    SYSTEM = 'System'
    PROCESSES = 'Processes'
    FREEZE = 'Freeze'
    KERNEL = 'Kernel'
    LOGS = 'Logs'
    WATCHDOG = 'Watchdog'
    QBITTORRENT = 'QBittorrent'
    UPDATES = 'Updates'

    DISCORD = 'Discord'
    WEB = 'Web'

    CACHE = 'Cache'
    SECURITY = 'Security'
    SYSTEMD = 'Systemd'
    CIRCUIT_BREAKER = 'CircuitBreaker'


KNOWN_COMPONENTS = frozenset(
    value
    for name, value in vars(Component).items()
    if not name.startswith('_') and isinstance(value, str)
)
