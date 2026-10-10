import os
import platform
import glob
from typing import Optional
from core.format import format_temperature, format_fan_speed


HWMON_FAN_BLACKLIST: set[str] = {
    'nouveau',
    'amdgpu',
    'radeon',
    'nvidia',
}


class SensorsMonitor:
    """Monitor hardware sensors (CPU temperature, fan speeds) - Linux only."""

    def __init__(self):
        """Initialize the sensors monitor."""
        self.is_linux = platform.system() == 'Linux'
        self.thermal_zones = []
        self._fan_scan_dirs = None
        self._discover_thermal_zones()

    def _discover_thermal_zones(self):
        """Discover available thermal zones on Linux."""
        if not self.is_linux:
            return

        thermal_base = '/sys/class/thermal'
        if os.path.exists(thermal_base):
            thermal_dirs = glob.glob(os.path.join(thermal_base, 'thermal_zone*'))
            for thermal_dir in thermal_dirs:
                zone_id = os.path.basename(thermal_dir)
                type_file = os.path.join(thermal_dir, 'type')
                temp_file = os.path.join(thermal_dir, 'temp')

                if os.path.exists(type_file) and os.path.exists(temp_file):
                    try:
                        with open(type_file) as f:
                            zone_type = f.read().strip()
                        self.thermal_zones.append({
                            'id': zone_id,
                            'path': thermal_dir,
                            'type': zone_type,
                            'temp_file': temp_file,
                        })
                    except OSError:
                        continue

    def is_available(self) -> bool:
        """
        Check if sensors monitoring is available.

        Returns:
            True if running on Linux and sensors are available, False otherwise
        """
        return self.is_linux and len(self.thermal_zones) > 0

    def get_cpu_temperature(self) -> Optional[float]:
        """
        Get CPU temperature in Celsius.

        Returns:
            CPU temperature in Celsius, or None if not available
        """
        if not self.is_linux or not self.thermal_zones:
            return None

        cpu_temp = None
        max_temp = None
        for zone in self.thermal_zones:
            try:
                with open(zone['temp_file']) as f:
                    temp_celsius = int(f.read().strip()) / 1000.0
            except (OSError, ValueError):
                continue

            if max_temp is None or temp_celsius > max_temp:
                max_temp = temp_celsius

            zone_type = zone['type'].lower()
            if 'cpu' in zone_type or 'processor' in zone_type or 'core' in zone_type:
                if cpu_temp is None or temp_celsius > cpu_temp:
                    cpu_temp = temp_celsius

        return cpu_temp if cpu_temp is not None else max_temp

    def get_all_temperatures(self) -> list[dict]:
        """
        Get temperatures from all thermal zones.

        Returns:
            List of dictionaries containing temperature information
        """
        temperatures = []

        if not self.is_available():
            return temperatures

        for zone in self.thermal_zones:
            try:
                with open(zone['temp_file']) as f:
                    temp_millidegrees = int(f.read().strip())
                    temp_celsius = temp_millidegrees / 1000.0

                temperatures.append({
                    'zone_id': zone['id'],
                    'type': zone['type'],
                    'temperature': temp_celsius,
                })
            except (OSError, ValueError):
                continue

        return temperatures

    def _hwmon_driver_name(self, hwmon_dir: str) -> Optional[str]:
        """
        Read the hwmon chip/driver name for a sysfs directory.

        Args:
            hwmon_dir: Path under /sys/class/hwmon

        Returns:
            Lowercased driver name, or None if unavailable
        """
        name_file = os.path.join(hwmon_dir, 'name')
        try:
            with open(name_file) as f:
                return f.read().strip().lower()
        except OSError:
            return None

    def _is_safe_fan_hwmon(self, hwmon_dir: str) -> bool:
        """
        Return whether fan*_input reads are safe for this hwmon device.

        Some GPU drivers (notably nouveau) can block forever in uninterruptible
        sleep when fan tachometers are read via sysfs, which stalls the whole
        monitoring process.
        """
        driver_name = self._hwmon_driver_name(hwmon_dir)
        if driver_name is None:
            return False
        return driver_name not in HWMON_FAN_BLACKLIST

    def get_fan_speeds(self) -> list[dict]:
        """
        Get fan speeds from hardware monitoring.

        Skips GPU hwmon devices whose fan sysfs nodes are known to hang
        (nouveau and other GPU drivers). Prefer nvidia-smi / vendor tools for
        GPU fans.

        Returns:
            List of dictionaries containing fan speed information
        """
        fans = []

        if not self.is_linux:
            return fans

        if self._fan_scan_dirs is None:
            self._fan_scan_dirs = self._discover_fan_scan_dirs()

        primary, fallback = self._fan_scan_dirs
        for scan_dir in primary:
            fans.extend(self._scan_fan_directory(scan_dir))
        if not fans:
            for scan_dir in fallback:
                fans.extend(self._scan_fan_directory(scan_dir))

        return fans

    def _discover_fan_scan_dirs(self):
        """Discover hwmon directories that are safe to read for fan speeds."""
        hwmon_base = '/sys/class/hwmon'
        if not os.path.exists(hwmon_base):
            return [], []

        primary = []
        fallback = []
        hwmon_dirs = sorted(glob.glob(os.path.join(hwmon_base, 'hwmon*')))
        for hwmon_dir in hwmon_dirs:
            if not self._is_safe_fan_hwmon(hwmon_dir):
                continue
            primary.append(hwmon_dir)
            device_dir = os.path.join(hwmon_dir, 'device')
            if os.path.exists(device_dir):
                fallback.append(device_dir)
        return primary, fallback

    def _scan_fan_directory(self, scan_dir: str) -> list[dict]:
        """
        Scan a directory for fan speed files and extract fan information.

        Args:
            scan_dir: Directory to scan for fan*_input files

        Returns:
            List of fan dictionaries with 'name', 'label', and 'speed_rpm' keys
        """
        fans = []
        fan_files = sorted(glob.glob(os.path.join(scan_dir, 'fan*_input')))

        for fan_file in fan_files:
            try:
                with open(fan_file) as f:
                    speed = int(f.read().strip())

                fan_name = os.path.basename(fan_file).replace('_input', '')

                label_file = fan_file.replace('_input', '_label')
                fan_label = fan_name
                if os.path.exists(label_file):
                    try:
                        with open(label_file) as lf:
                            fan_label = lf.read().strip()
                    except OSError:
                        pass

                fans.append({
                    'name': fan_name,
                    'label': fan_label,
                    'speed_rpm': speed,
                })
            except (OSError, ValueError):
                continue

        return fans

    def get_sensor_summary(self) -> dict:
        """
        Get a summary of all sensor readings.

        Returns:
            Dictionary containing sensor summary
        """
        cpu_temp = self.get_cpu_temperature()
        all_temps = self.get_all_temperatures()
        fans = self.get_fan_speeds()

        return {
            'available': self.is_available(),
            'cpu_temperature': cpu_temp,
            'all_temperatures': all_temps,
            'fan_speeds': fans,
            'thermal_zones_count': len(self.thermal_zones),
            'fans_count': len(fans),
        }

    def get_sensor_info(self) -> dict:
        """
        Get comprehensive sensor information.

        Returns:
            Dictionary containing all sensor information
        """
        return {
            'platform': platform.system(),
            'sensors_available': self.is_available(),
            'thermal_zones': [
                {
                    'id': zone['id'],
                    'type': zone['type'],
                }
                for zone in self.thermal_zones
            ],
            'cpu_temperature': self.get_cpu_temperature(),
            'all_temperatures': self.get_all_temperatures(),
            'fan_speeds': self.get_fan_speeds(),
        }

    def format_temperature(self, temp_celsius: float) -> str:
        """
        Format temperature for display.

        Args:
            temp_celsius: Temperature in Celsius

        Returns:
            Formatted temperature string
        """
        return format_temperature(temp_celsius)

    def format_fan_speed(self, speed_rpm: int) -> str:
        """
        Format fan speed for display.

        Args:
            speed_rpm: Fan speed in RPM

        Returns:
            Formatted fan speed string
        """
        return format_fan_speed(speed_rpm)

