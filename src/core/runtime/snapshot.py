"""
Per-cycle readings shared by every handler.
"""

from dataclasses import dataclass, field
from typing import Any, Optional


@dataclass
class Snapshot:
    """One monitoring cycle's readings. Collectors fill it in; handlers only read it."""

    current_time: float
    cpu_percent: float = 0.0
    cpu_per_core: list[float] = field(default_factory=list)
    memory_info: dict[str, Any] = field(default_factory=dict)
    memory_percent: float = 0.0
    swap_info: dict[str, Any] = field(default_factory=dict)
    gpu_utilization: Optional[dict[str, float]] = None
    gpu_data: Optional[dict[str, Any]] = None
    all_partitions: list[dict[str, Any]] = field(default_factory=list)
    total_disk: dict[str, Any] = field(default_factory=dict)
    disk_percent: float = 0.0
    cpu_temp: Optional[float] = None
    network_bandwidth: Optional[float] = None
    bandwidth: Optional[dict[str, float]] = None
    disk_io_data: Optional[dict[str, Any]] = None
    network_io_data: Optional[dict[str, Any]] = None
    top_cpu_processes: Optional[list[dict[str, Any]]] = None
    top_memory_processes: Optional[list[dict[str, Any]]] = None
