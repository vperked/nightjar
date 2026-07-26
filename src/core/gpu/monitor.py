import subprocess
from typing import Dict, List, Optional
from core.format import format_megabytes
from core.observability import Component, get_logger

logger = get_logger(Component.GPU)


class GpuMonitor:
    """Monitor NVIDIA GPU usage and statistics using nvidia-smi."""

    def __init__(self):
        """Initialize the GPU monitor."""
        self.nvidia_smi_available = self._check_nvidia_smi()

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

    def get_gpu_count(self) -> int:
        """
        Get the number of GPUs in the system.

        Returns:
            Number of GPUs, or 0 if nvidia-smi is not available
        """
        if not self.nvidia_smi_available:
            return 0

        output = self._run_nvidia_smi('count')
        if output:
            try:
                return int(output.strip())
            except ValueError:
                return 0
        return 0

    def get_gpu_info(self, gpu_index: int = 0) -> Dict:
        """
        Get information about a specific GPU.

        Args:
            gpu_index: Index of the GPU (0-based). Must be non-negative.

        Returns:
            Dictionary containing GPU information
        """
        if not self.nvidia_smi_available:
            return {}

        gpu_count = self.get_gpu_count()
        if gpu_count == 0 or gpu_index < 0 or gpu_index >= gpu_count:
            return {}

        name = self._run_nvidia_smi('name', format='csv,noheader')
        if name:
            names = name.split('\n')
            if gpu_index < len(names):
                gpu_name = names[gpu_index].strip()
            else:
                gpu_name = 'N/A'
        else:
            gpu_name = 'N/A'

        driver_version = self._run_nvidia_smi('driver_version', format='csv,noheader')
        if driver_version:
            driver_version = driver_version.split('\n')[0].strip() if driver_version else 'N/A'
        else:
            driver_version = 'N/A'

        uuid = self._run_nvidia_smi('uuid', format='csv,noheader')
        if uuid:
            uuids = uuid.split('\n')
            if gpu_index < len(uuids):
                gpu_uuid = uuids[gpu_index].strip()
            else:
                gpu_uuid = 'N/A'
        else:
            gpu_uuid = 'N/A'

        return {
            'index': gpu_index,
            'name': gpu_name,
            'driver_version': driver_version,
            'uuid': gpu_uuid,
        }

    def get_gpu_utilization(self, gpu_index: int = 0) -> Dict:
        """
        Get GPU utilization (GPU and memory).

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary containing GPU and memory utilization percentages
        """
        if not self.nvidia_smi_available:
            return {'gpu': 0.0, 'memory': 0.0}

        gpu_count = self.get_gpu_count()
        if gpu_count == 0 or gpu_index < 0 or gpu_index >= gpu_count:
            return {'gpu': 0.0, 'memory': 0.0}

        gpu_util = self._run_nvidia_smi('utilization.gpu')
        memory_util = self._run_nvidia_smi('utilization.memory')

        gpu_percent = 0.0
        memory_percent = 0.0

        if gpu_util:
            utils = gpu_util.split('\n')
            if gpu_index < len(utils):
                try:
                    gpu_percent = float(utils[gpu_index].strip())
                except ValueError:
                    gpu_percent = 0.0

        if memory_util:
            utils = memory_util.split('\n')
            if gpu_index < len(utils):
                try:
                    memory_percent = float(utils[gpu_index].strip())
                except ValueError:
                    memory_percent = 0.0

        return {
            'gpu': gpu_percent,
            'memory': memory_percent,
        }

    def get_gpu_memory(self, gpu_index: int = 0) -> Dict:
        """
        Get GPU memory information.

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary containing memory information in MB
        """
        if not self.nvidia_smi_available:
            return {'total': 0, 'used': 0, 'free': 0, 'percent': 0.0}

        gpu_count = self.get_gpu_count()
        if gpu_count == 0 or gpu_index < 0 or gpu_index >= gpu_count:
            return {'total': 0, 'used': 0, 'free': 0, 'percent': 0.0}

        total_mem = self._run_nvidia_smi('memory.total')
        used_mem = self._run_nvidia_smi('memory.used')
        free_mem = self._run_nvidia_smi('memory.free')

        total = 0
        used = 0
        free = 0

        if total_mem:
            totals = total_mem.split('\n')
            if gpu_index < len(totals):
                try:
                    total = int(float(totals[gpu_index].strip()))
                except ValueError:
                    total = 0

        if used_mem:
            useds = used_mem.split('\n')
            if gpu_index < len(useds):
                try:
                    used = int(float(useds[gpu_index].strip()))
                except ValueError:
                    used = 0

        if free_mem:
            frees = free_mem.split('\n')
            if gpu_index < len(frees):
                try:
                    free = int(float(frees[gpu_index].strip()))
                except ValueError:
                    free = 0

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
        if not self.nvidia_smi_available:
            return 0.0

        gpu_count = self.get_gpu_count()
        if gpu_count == 0 or gpu_index < 0 or gpu_index >= gpu_count:
            return 0.0

        temp = self._run_nvidia_smi('temperature.gpu')
        if temp:
            temps = temp.split('\n')
            if gpu_index < len(temps):
                try:
                    return float(temps[gpu_index].strip())
                except ValueError:
                    return 0.0
        return 0.0

    def get_gpu_power(self, gpu_index: int = 0) -> Dict:
        """
        Get GPU power consumption information.

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary containing power draw and power limit in watts
        """
        if not self.nvidia_smi_available:
            return {'draw': 0.0, 'limit': 0.0}

        gpu_count = self.get_gpu_count()
        if gpu_count == 0 or gpu_index < 0 or gpu_index >= gpu_count:
            return {'draw': 0.0, 'limit': 0.0}

        power_draw = self._run_nvidia_smi('power.draw')
        power_limit = self._run_nvidia_smi('power.limit')

        draw = 0.0
        limit = 0.0

        if power_draw:
            draws = power_draw.split('\n')
            if gpu_index < len(draws):
                try:
                    draw = float(draws[gpu_index].strip())
                except ValueError:
                    draw = 0.0

        if power_limit:
            limits = power_limit.split('\n')
            if gpu_index < len(limits):
                try:
                    limit = float(limits[gpu_index].strip())
                except ValueError:
                    limit = 0.0

        return {
            'draw': draw,
            'limit': limit,
        }

    def get_gpu_clocks(self, gpu_index: int = 0) -> Dict:
        """
        Get GPU clock speeds.

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary containing graphics and memory clock speeds in MHz
        """
        if not self.nvidia_smi_available:
            return {'graphics': 0, 'memory': 0}

        gpu_count = self.get_gpu_count()
        if gpu_count == 0 or gpu_index < 0 or gpu_index >= gpu_count:
            return {'graphics': 0, 'memory': 0}

        graphics_clock = self._run_nvidia_smi('clocks.current.graphics')
        memory_clock = self._run_nvidia_smi('clocks.current.memory')

        graphics = 0
        memory = 0

        if graphics_clock:
            clocks = graphics_clock.split('\n')
            if gpu_index < len(clocks):
                try:
                    graphics = int(float(clocks[gpu_index].strip()))
                except ValueError:
                    graphics = 0

        if memory_clock:
            clocks = memory_clock.split('\n')
            if gpu_index < len(clocks):
                try:
                    memory = int(float(clocks[gpu_index].strip()))
                except ValueError:
                    memory = 0

        return {
            'graphics': graphics,
            'memory': memory,
        }

    def get_all_gpus_info(self) -> List[Dict]:
        """
        Get information for all GPUs.

        Returns:
            List of dictionaries containing information for each GPU
        """
        gpu_count = self.get_gpu_count()
        gpus_info = []

        for i in range(gpu_count):
            gpu_info = self.get_gpu_info(i)
            utilization = self.get_gpu_utilization(i)
            memory = self.get_gpu_memory(i)
            temperature = self.get_gpu_temperature(i)
            power = self.get_gpu_power(i)
            clocks = self.get_gpu_clocks(i)

            gpu_data = {
                **gpu_info,
                'utilization': utilization,
                'memory': memory,
                'temperature': temperature,
                'power': power,
                'clocks': clocks,
            }
            gpus_info.append(gpu_data)

        return gpus_info

    def get_gpu_comprehensive(self, gpu_index: int = 0) -> Dict:
        """
        Get comprehensive information for a specific GPU.

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary containing all GPU information
        """
        return {
            'info': self.get_gpu_info(gpu_index),
            'utilization': self.get_gpu_utilization(gpu_index),
            'memory': self.get_gpu_memory(gpu_index),
            'temperature': self.get_gpu_temperature(gpu_index),
            'power': self.get_gpu_power(gpu_index),
            'clocks': self.get_gpu_clocks(gpu_index),
        }

    def get_gpu_memory_human(self, gpu_index: int = 0) -> Dict[str, str]:
        """
        Get GPU memory information in human-readable format.

        Args:
            gpu_index: Index of the GPU (default: 0)

        Returns:
            Dictionary with memory values formatted as strings (MB, GB)
        """
        gpu_count = self.get_gpu_count()
        if gpu_count == 0 or gpu_index < 0 or gpu_index >= gpu_count:
            return {'total': '0 MB', 'used': '0 MB', 'free': '0 MB', 'percent': '0.0%'}

        memory = self.get_gpu_memory(gpu_index)

        return {
            'total': format_megabytes(memory['total']),
            'used': format_megabytes(memory['used']),
            'free': format_megabytes(memory['free']),
            'percent': f"{memory['percent']:.1f}%",
        }

