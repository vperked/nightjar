import csv
import io
import subprocess
from typing import Dict, List, Optional
from core.observability import Component, get_logger

logger = get_logger(Component.GPU)

COMPREHENSIVE_QUERY = (
    'name,driver_version,uuid,utilization.gpu,utilization.memory,'
    'memory.total,memory.used,memory.free,temperature.gpu,'
    'power.draw,power.limit,clocks.current.graphics,clocks.current.memory'
)


class GpuMonitor:
    """Monitor NVIDIA GPU usage and statistics using nvidia-smi."""

    def __init__(self):
        """Initialize the GPU monitor."""
        self.nvidia_smi_available = self._check_nvidia_smi()
        self._gpu_count = None

    def _check_nvidia_smi(self) -> bool:
        """
        Check if nvidia-smi is available.

        Returns:
            True if nvidia-smi is available, False otherwise
        """
        try:
            result = subprocess.run(
                ['nvidia-smi', '--version'],
                capture_output=True,
                text=True,
                timeout=5
            )
            return result.returncode == 0
        except (FileNotFoundError, subprocess.TimeoutExpired):
            return False

    def _run_nvidia_smi(self, query: str, format: str = 'csv,noheader,nounits') -> Optional[str]:
        """
        Run nvidia-smi with a query.

        Args:
            query: Query string for nvidia-smi
            format: Output format (default: csv,noheader,nounits)

        Returns:
            Command output as string, or None if command fails
        """
        if not self.nvidia_smi_available:
            return None

        try:
            result = subprocess.run(
                ['nvidia-smi', f'--query-gpu={query}', f'--format={format}'],
                capture_output=True,
                text=True,
                timeout=10
            )
            if result.returncode == 0:
                return result.stdout.strip()
            return None
        except (subprocess.TimeoutExpired, subprocess.SubprocessError, FileNotFoundError) as e:
            logger.warning(f"GPU query failed: {e}")
            return None

    def _query_rows(self, query: str) -> List[List[str]]:
        """
        Run nvidia-smi and parse CSV rows for each GPU.

        Args:
            query: Comma-separated nvidia-smi query fields

        Returns:
            List of field lists, one per GPU
        """
        output = self._run_nvidia_smi(query)
        if not output:
            return []

        rows = []
        reader = csv.reader(io.StringIO(output))
        for row in reader:
            if row:
                rows.append([cell.strip() for cell in row])
        return rows

    @staticmethod
    def _to_float(value: str, default: float = 0.0) -> float:
        """Parse a nvidia-smi numeric field, treating N/A as default."""
        try:
            return float(value)
        except (TypeError, ValueError):
            return default

    @staticmethod
    def _to_int(value: str, default: int = 0) -> int:
        """Parse a nvidia-smi integer field, treating N/A as default."""
        try:
            return int(float(value))
        except (TypeError, ValueError):
            return default

    def get_gpu_count(self) -> int:
        """
        Get the number of GPUs in the system.

        Returns:
            Number of GPUs, or 0 if nvidia-smi is not available
        """
        if not self.nvidia_smi_available:
            return 0

        if self._gpu_count is not None:
            return self._gpu_count

        rows = self._query_rows('name')
        self._gpu_count = len(rows)
        return self._gpu_count

    def _require_gpu(self, gpu_index: int) -> bool:
        """Return True if nvidia-smi is available and gpu_index is in range."""
        if not self.nvidia_smi_available or gpu_index < 0:
            return False
        return gpu_index < self.get_gpu_count()

    def get_gpu_info(self, gpu_index: int = 0) -> Dict:
        """
        Get information about a specific GPU.

        Args:
            gpu_index: Index of the GPU (0-based). Must be non-negative.

        Returns:
            Dictionary containing GPU information
        """
        if not self._require_gpu(gpu_index):
            return {}

        rows = self._query_rows('name,driver_version,uuid')
        if gpu_index >= len(rows):
            return {}

        row = rows[gpu_index]
        while len(row) < 3:
            row.append('N/A')

        return {
            'index': gpu_index,
            'name': row[0] or 'N/A',
            'driver_version': row[1] or 'N/A',
            'uuid': row[2] or 'N/A',
        }

    def get_gpu_utilization(self, gpu_index: int = 0) -> Dict:
        """
        Get GPU utilization (GPU and memory).

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary containing GPU and memory utilization percentages
        """
        if not self._require_gpu(gpu_index):
            return {'gpu': 0.0, 'memory': 0.0}

        rows = self._query_rows('utilization.gpu,utilization.memory')
        if gpu_index >= len(rows):
            return {'gpu': 0.0, 'memory': 0.0}

        row = rows[gpu_index]
        while len(row) < 2:
            row.append('0')

        return {
            'gpu': self._to_float(row[0]),
            'memory': self._to_float(row[1]),
        }

    def get_gpu_memory(self, gpu_index: int = 0) -> Dict:
        """
        Get GPU memory information.

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary containing memory information in MB
        """
        if not self._require_gpu(gpu_index):
            return {'total': 0, 'used': 0, 'free': 0, 'percent': 0.0}

        rows = self._query_rows('memory.total,memory.used,memory.free')
        if gpu_index >= len(rows):
            return {'total': 0, 'used': 0, 'free': 0, 'percent': 0.0}

        row = rows[gpu_index]
        while len(row) < 3:
            row.append('0')

        total = self._to_int(row[0])
        used = self._to_int(row[1])
        free = self._to_int(row[2])
        percent = (used / total * 100) if total > 0 else 0.0

        return {
            'total': total,
            'used': used,
            'free': free,
            'percent': percent,
        }

    def get_gpu_temperature(self, gpu_index: int = 0) -> float:
        """
        Get GPU temperature.

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            GPU temperature in Celsius, or 0.0 if unavailable
        """
        if not self._require_gpu(gpu_index):
            return 0.0

        rows = self._query_rows('temperature.gpu')
        if gpu_index >= len(rows) or not rows[gpu_index]:
            return 0.0
        return self._to_float(rows[gpu_index][0])

    def get_gpu_power(self, gpu_index: int = 0) -> Dict:
        """
        Get GPU power consumption information.

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary containing power draw and power limit in watts
        """
        if not self._require_gpu(gpu_index):
            return {'draw': 0.0, 'limit': 0.0}

        rows = self._query_rows('power.draw,power.limit')
        if gpu_index >= len(rows):
            return {'draw': 0.0, 'limit': 0.0}

        row = rows[gpu_index]
        while len(row) < 2:
            row.append('0')

        return {
            'draw': self._to_float(row[0]),
            'limit': self._to_float(row[1]),
        }

    def get_gpu_clocks(self, gpu_index: int = 0) -> Dict:
        """
        Get GPU clock speeds.

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary containing graphics and memory clock speeds in MHz
        """
        if not self._require_gpu(gpu_index):
            return {'graphics': 0, 'memory': 0}

        rows = self._query_rows('clocks.current.graphics,clocks.current.memory')
        if gpu_index >= len(rows):
            return {'graphics': 0, 'memory': 0}

        row = rows[gpu_index]
        while len(row) < 2:
            row.append('0')

        return {
            'graphics': self._to_int(row[0]),
            'memory': self._to_int(row[1]),
        }

    def get_gpu_comprehensive(self, gpu_index: int = 0) -> Dict:
        """
        Get comprehensive information for a specific GPU in a single nvidia-smi call.

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary containing all GPU information
        """
        if not self.nvidia_smi_available or gpu_index < 0:
            return {}

        rows = self._query_rows(COMPREHENSIVE_QUERY)
        self._gpu_count = len(rows)
        if gpu_index >= len(rows):
            return {}

        row = rows[gpu_index]
        while len(row) < 13:
            row.append('')

        total = self._to_int(row[5])
        used = self._to_int(row[6])
        free = self._to_int(row[7])
        percent = (used / total * 100) if total > 0 else 0.0

        return {
            'info': {
                'index': gpu_index,
                'name': row[0] or 'N/A',
                'driver_version': row[1] or 'N/A',
                'uuid': row[2] or 'N/A',
            },
            'utilization': {
                'gpu': self._to_float(row[3]),
                'memory': self._to_float(row[4]),
            },
            'memory': {
                'total': total,
                'used': used,
                'free': free,
                'percent': percent,
            },
            'temperature': self._to_float(row[8]),
            'power': {
                'draw': self._to_float(row[9]),
                'limit': self._to_float(row[10]),
            },
            'clocks': {
                'graphics': self._to_int(row[11]),
                'memory': self._to_int(row[12]),
            },
        }
