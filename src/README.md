# Server Monitor - Source Code Documentation

A comprehensive server monitoring system with Discord alerts, web dashboard, and system resource tracking.

> **Latest Update:** January 2026 - Kernel monitoring, automatic disk cleanup, and comprehensive code review fixes

## Directory Structure

```
src/
├── main.py                 # Main entry point and monitoring loop
├── logger.py              # Logging configuration and utilities
├── cache/                 # Cache management
│   └── cache.py
├── discord/               # Discord webhook integration
│   └── webhook.py
├── monitor/               # System monitoring modules
│   ├── cpu.py            # CPU usage and frequency monitoring
│   ├── memory.py         # Memory and swap monitoring
│   ├── disk.py           # Disk usage, directory monitoring, and automatic cleanup
│   ├── network.py        # Network bandwidth and I/O monitoring
│   ├── gpu.py            # NVIDIA GPU monitoring (nvidia-smi)
│   ├── sensors.py        # Temperature and fan speed monitoring
│   ├── system.py         # System info, processes, and uptime
│   ├── processes.py      # Process resource usage tracking
│   ├── freeze.py         # System freeze detection
│   ├── kernel.py         # Kernel crash detection and system stability monitoring
│   ├── log.py            # Log file monitoring and cleanup
│   ├── qbittorrent.py    # qBittorrent Web API integration
│   └── media.py          # Media directory monitoring
├── util/                  # Utility modules
│   ├── util.py           # Formatting and helper functions
│   ├── systemd.py        # Systemd service installation
│   └── error_handling.py # Error handling utilities
└── web/                   # Web dashboard
    ├── server.py         # HTTP server and API endpoints
    └── site/             # Frontend files
        ├── index.html
        └── js/
            └── main.js
```

## 🚀 Quick Start

### Basic Usage

```bash
# Run the monitor
python3 src/main.py

# Run self-test (checks all monitors and sends sample embeds)
python3 src/main.py --test

# Install as systemd service
sudo python3 src/main.py --install-systemd
```

### Configuration

1. Copy `env.recommended` to `.env` in the project root
2. Configure your Discord webhook URL
3. Adjust thresholds and settings as needed
4. Enable optional features (qBittorrent, media monitoring, etc.)

## 📋 Main Components

### `main.py`
The core monitoring application that:
- Initializes all monitor modules
- Runs continuous monitoring loop
- Sends Discord alerts when thresholds are exceeded
- Provides web dashboard
- Handles graceful shutdown

**Key Features:**
- CPU, Memory, Disk, Network, GPU monitoring
- Temperature alerts with cause analysis
- Process resource tracking
- qBittorrent integration (optional)
- Media directory size monitoring (optional)
- System freeze detection (optional)
- Startup self-test

### Monitor Modules

#### `monitor/cpu.py` - CPU Monitoring
- Overall and per-core CPU usage
- CPU frequency tracking
- Load average monitoring

#### `monitor/memory.py` - Memory Monitoring
- RAM usage and statistics
- Swap memory tracking
- Memory pressure detection

#### `monitor/disk.py` - Disk Monitoring
- Disk usage per partition
- Directory size monitoring
- Largest directories identification
- **Automatic disk cleanup** - Proactive cleanup when usage exceeds thresholds
  - Normal cleanup at 50% usage (configurable)
  - Emergency cleanup at 95% usage (configurable)
  - Temporary file cleanup (`/tmp`, `/var/tmp`)
  - Log file cleanup (via log monitor integration)
  - Cache directory cleanup (`/var/cache`)
  - Prevents disk from reaching 100% usage

#### `monitor/network.py` - Network Monitoring
- Bandwidth usage (upload/download)
- Network I/O statistics
- Connection monitoring

#### `monitor/gpu.py` - GPU Monitoring
- NVIDIA GPU utilization (via nvidia-smi)
- GPU memory usage
- Temperature and power monitoring
- Clock speeds

#### `monitor/sensors.py` - Sensor Monitoring
- CPU temperature
- Fan speeds
- System sensor data

#### `monitor/system.py` - System Information
- System uptime
- Hostname and platform info
- Process count
- Top processes by CPU/memory

#### `monitor/kernel.py` - Kernel Crash & Stability Monitoring
- **Proactive crash detection** - Early warning signs before crashes
- Kernel panic and oops detection
- System stability risk assessment
- Hardware error detection (EDAC, MCE)
- Memory pressure monitoring (OOM killer detection)
- Disk I/O error detection
- System lockup detection
- Unexpected reboot detection
- Boot time tracking
- Risk scoring with recommendations

#### `monitor/log.py` - Log File Monitoring
- Log file size monitoring
- Log cleanup integration with disk cleanup
- Directory log analysis

#### `monitor/processes.py` - Process Monitoring
- Top processes by resource usage
- Process details (PID, name, user, cmdline)
- Resource consumption tracking

#### `monitor/freeze.py` - Freeze Detection
- Heartbeat monitoring
- Metric update tracking
- System responsiveness checks
- Configurable thresholds

#### `monitor/qbittorrent.py` - qBittorrent Integration
- Torrent list and status
- Download/upload speed tracking
- Download completion notifications
- Seeding status monitoring

#### `monitor/media.py` - Media Directory Monitoring
- Directory size tracking
- Subdirectory analysis
- Size threshold alerts

### Discord Integration (`discord/webhook.py`)

Sends rich embed alerts to Discord for:
- CPU usage alerts
- Memory usage alerts
- Disk usage alerts (via `DISK_WEBHOOK` if configured)
- Network bandwidth alerts
- Temperature alerts (with cause analysis)
- Process resource alerts
- Directory size alerts
- qBittorrent notifications (via `QBITTORRENT_WEBHOOK` if configured)
- System freeze alerts
- Kernel crash and stability alerts (via `CRASH_WEBHOOK` if configured)
- Disk cleanup notifications

**Separate Webhooks:**
- `WEBHOOK` - Main alerts (default)
- `DISK_WEBHOOK` - Disk-related alerts (usage, cleanup, directory size)
- `CRASH_WEBHOOK` - Kernel crash, stability, and reboot alerts
- `QBITTORRENT_WEBHOOK` - qBittorrent-specific notifications

**Features:**
- Consistent embed styling (red color scheme)
- Detailed field information
- Timestamps and footers
- Error handling and retry logic
- Rate limiting (30 calls per 60 seconds) to prevent Discord API limits
- Fallback logging to file when Discord is unavailable

### Web Dashboard (`web/server.py`)

Provides a web interface at `http://localhost:8080` (configurable):
- Real-time system statistics
- CPU, Memory, Disk, Network data
- GPU information (if available)
- Temperature readings
- Process information

**API Endpoints:**
- `GET /` - Web dashboard
- `GET /api/stats` - JSON API for system statistics
- `GET /health` - Comprehensive health check endpoint with system status
- `GET /js/main.js` - JavaScript frontend

**Health Check Endpoint (`/health`):**
Returns detailed health status with checks for:
- **CPU**: Usage percentage (warns if >95%)
- **Memory**: Usage percentage and available memory (warns if >95% or <100MB available)
- **Disk**: Usage percentage and free space (warns if >95% or <1GB free)
- **System**: Uptime and boot time
- **Temperature**: CPU temperature if sensors available (warns if >90°C)

Response status codes:
- `200 OK` - All systems healthy
- `503 Service Unavailable` - System degraded (high resource usage)
- `500 Internal Server Error` - Critical failure

Example response:
```json
{
  "status": "ok",
  "timestamp": 1767940917.3821733,
  "timestamp_iso": "2026-01-09T12:34:56.789",
  "checks": {
    "cpu": {"status": "ok", "usage_percent": 45.2},
    "memory": {"status": "ok", "usage_percent": 62.1, "available_gb": 12.5},
    "disk": {"status": "ok", "usage_percent": 78.3, "free_gb": 450.2},
    "system": {"status": "ok", "uptime_hours": 120.5},
    "temperature": {"status": "ok", "cpu_temp_celsius": 65.3}
  }
}
```

### Utilities

#### `util/util.py`
Formatting functions:
- `format_bytes()` - Human-readable byte formatting
- `format_bytes_per_second()` - Bandwidth formatting
- `format_megabytes()` - MB formatting

#### `util/systemd.py`
Systemd service management:
- Service file generation
- Automatic installation
- Service replacement (if already exists)
- User and environment configuration
- Permission checking and interactive fixing
- Smart working directory selection based on user permissions

#### `util/error_handling.py`
Standardized error handling utilities:
- Connection error decorators
- Logging helpers
- Error pattern standardization

#### `util/security.py`
Security utilities:
- Path validation and traversal protection
- Safe subprocess execution
- Environment variable validation
- Security checks for file operations

#### `cache/cache.py`
Cache management:
- Startup cache clearing
- Exit cache cleanup
- Directory cache management

## Configuration

All configuration is done via environment variables (`.env` file):

### Required
- `WEBHOOK` - Discord webhook URL for main alerts

### Optional Webhooks
- `DISK_WEBHOOK` - Separate webhook for disk-related alerts (usage, cleanup, directory size)
- `CRASH_WEBHOOK` - Separate webhook for kernel crash and stability alerts
- `QBITTORRENT_WEBHOOK` - Separate webhook for qBittorrent notifications

### Optional Features
- `QBITTORRENT_MONITOR` - Enable qBittorrent monitoring (true/false)
- `MEDIA_MONITOR_ENABLED` - Enable media directory monitoring (true/false)
- `FREEZE_MONITOR_ENABLED` - Enable freeze detection (true/false)
- `SELF_TEST_ON_START` - Run self-test on startup (true/false, default: true)

### Thresholds
- `CPU_THRESHOLD` - CPU usage alert threshold (default: 80.0%)
- `MEMORY_THRESHOLD` - Memory usage threshold (default: 80.0%)
- `GPU_THRESHOLD` - GPU utilization threshold (default: 80.0%)
- `DISK_THRESHOLD` - Disk usage alert threshold (default: 80.0%)
- `DISK_CLEANUP_THRESHOLD` - Disk usage threshold to trigger cleanup (default: 50.0%)
- `DISK_EMERGENCY_THRESHOLD` - Disk usage threshold for emergency cleanup (default: 95.0%, clamped 90-99%)
- `TEMP_THRESHOLD` - Temperature threshold in Celsius (default: 80.0°C)
- `NETWORK_BANDWIDTH_THRESHOLD` - Network bandwidth in MB/s (default: 1000.0)
- `PROCESS_CPU_THRESHOLD` - Per-process CPU threshold (default: 50.0%)
- `PROCESS_MEMORY_THRESHOLD` - Per-process memory threshold (default: 10.0%)

### Timing
- `CHECK_INTERVAL` - Monitoring loop interval in seconds (default: 5.0)
- `ALERT_COOLDOWN` - Seconds between alerts for same metric (default: 300.0)
- `TEMP_ALERT_DURATION` - Seconds temp must stay high before alert (default: 30.0)
- `DISK_CLEANUP_COOLDOWN` - Seconds between disk cleanup attempts (default: 3600.0)
- `DISK_EMERGENCY_COOLDOWN` - Seconds between emergency cleanup attempts (default: 1800.0)
- `KERNEL_CHECK_INTERVAL` - Loop iterations between kernel crash checks (default: 30)
- `KERNEL_STABILITY_CHECK_INTERVAL` - Loop iterations between stability checks (default: 10)
- `KERNEL_CHECK_SINCE_MINUTES` - Minutes to look back in kernel logs (default: 60)

### qBittorrent Settings
- `QBITTORRENT_HOST` - qBittorrent Web UI host (default: localhost)
- `QBITTORRENT_PORT` - qBittorrent Web UI port (default: 8080)
- `QBITTORRENT_USERNAME` - Web UI username (default: admin)
- `QBITTORRENT_PASSWORD` - Web UI password (required if enabled)
- `QBITTORRENT_WEBHOOK` - Separate webhook for qBittorrent notifications

### Media Directory Settings
- `MEDIA_DIRECTORY` - Directory to monitor (default: /home/media)
- `MEDIA_DIRECTORY_THRESHOLD_GB` - Size threshold in GB (default: 500.0)
- `MEDIA_CHECK_INTERVAL` - Check every N loop iterations (default: 10)

### Web Server
- `WEB_PORT` - Web dashboard port (default: 8080)

### Error Recovery
- `ALERT_LOG_PATH` - Path for fallback alert logging when Discord fails (default: /var/log/server-monitor-alerts.log)

### Disk Cleanup Settings
- `DISK_CLEANUP_THRESHOLD` - Start cleanup when disk usage exceeds this % (default: 50.0%)
- `DISK_EMERGENCY_THRESHOLD` - Emergency cleanup when disk usage exceeds this % (default: 95.0%)
- `DISK_CLEANUP_COOLDOWN` - Seconds between normal cleanup attempts (default: 3600.0 = 1 hour)
- `DISK_EMERGENCY_COOLDOWN` - Seconds between emergency cleanup attempts (default: 1800.0 = 30 min)

**Cleanup Behavior:**
- Normal cleanup: Removes files older than 7 days, then 3 days, then 1 day, then all temp files
- Emergency cleanup: Aggressively removes all temporary files, cleans logs, and cache directories
- Prevents disk from reaching 100% usage which can cause system crashes

### Kernel Monitoring Settings
- `KERNEL_CHECK_INTERVAL` - Check for crashes every N loop iterations (default: 30)
- `KERNEL_STABILITY_CHECK_INTERVAL` - Check stability every N loop iterations (default: 10)
- `KERNEL_CHECK_SINCE_MINUTES` - Look back N minutes in kernel logs (default: 60)

**Monitoring Features:**
- Detects kernel panics, oops, and crashes
- Proactive early warning detection (hardware errors, memory pressure, I/O errors)
- System stability risk scoring (low, medium, high, critical)
- Unexpected reboot detection
- Boot time tracking

See `env.recommended` for complete configuration options.

## 🔧 Command Line Options

```bash
# Run normally
python3 src/main.py

# Run self-test (checks monitors, sends sample embeds)
python3 src/main.py --test

# Install as systemd service
sudo python3 src/main.py --install-systemd

# Force systemd service replacement
sudo python3 src/main.py --install-systemd --force

# Note: The installer will check permissions and prompt to fix them if needed
```

## Monitoring Features

### Automatic Alerts
The system automatically sends Discord alerts when:
- CPU usage exceeds threshold
- Memory usage exceeds threshold
- Disk usage exceeds threshold
- GPU utilization exceeds threshold
- Temperature exceeds threshold for specified duration
- Network bandwidth exceeds threshold
- Individual processes exceed CPU/memory thresholds
- Media directory exceeds size threshold
- System freeze detected (if enabled)
- Kernel crash or panic detected
- System stability risk detected (high/critical)
- Unexpected system reboot detected
- Early warning signs detected (hardware errors, memory pressure, etc.)

### Automatic Disk Cleanup
The system automatically cleans up disk space when:
- Disk usage exceeds `DISK_CLEANUP_THRESHOLD` (default: 50%) - Normal cleanup
- Disk usage exceeds `DISK_EMERGENCY_THRESHOLD` (default: 95%) - Emergency cleanup
- Cleanup targets: `/tmp`, `/var/tmp`, log files, cache directories
- Prevents disk from reaching 100% usage (which can cause system crashes)

### Alert Cooldown
Alerts for the same metric are rate-limited by `ALERT_COOLDOWN` to prevent spam.

### Temperature Analysis
When temperature alerts trigger, the system analyzes and reports likely causes:
- CPU usage patterns
- Download/media activity
- Fan speeds
- Disk I/O activity
- Network activity
- Process activity

## Architecture

### Design Patterns
- **Modular Monitors**: Each system component has its own monitor class
- **Observer Pattern**: Monitors collect data, main loop processes alerts
- **Singleton Pattern**: Web server uses global monitor instances
- **Factory Pattern**: Monitor initialization based on configuration
- **Dependency Injection**: Main function supports optional monitor instances for testing

### Code Quality
- **Type Hints**: Comprehensive type annotations throughout
- **Constants**: Magic numbers replaced with named constants
- **Error Handling**: Specific exception types with proper logging
- **Code Reuse**: Helper functions eliminate duplication
- **Package Structure**: Proper `__init__.py` files for all packages

### Error Handling
- Specific exceptions for known error types (ValueError, OSError, ConnectionError, etc.)
- Broad Exception catching for unexpected errors (with proper logging)
- Connection errors logged at debug level (expected transient)
- Critical errors logged at error level
- Fallback alert logging to file when Discord webhook fails
- Graceful degradation: individual monitor failures don't stop the entire system

### Threading
- Main monitoring loop runs in main thread
- Web server runs in daemon thread
- Freeze monitor watchdog runs in separate thread (if enabled)
- Thread-safe operations with locks where needed

## 🔒 Security Considerations

- Environment variables for sensitive data (passwords, API keys)
- Path traversal protection in web server
- Input validation on monitor methods
- No sensitive data logged
- Environment variable validation (length checks, format validation)
- Path validation for media directory (prevents access to /proc, /sys, path traversal)
- Subprocess calls use list form (not shell=True) to prevent injection
- All subprocess calls have timeout protection

## 📝 Logging

Logs are written to `server_monitor.log` with levels:
- **INFO**: Normal operation, status updates
- **WARNING**: Recoverable errors, threshold alerts
- **ERROR**: Critical failures, unexpected errors
- **DEBUG**: Detailed diagnostic information

## 🧪 Testing

Run the self-test to verify all monitors:
```bash
python3 src/main.py --test
```

This will:
- Test all monitor initialization
- Verify monitor data collection
- Send sample Discord embeds (if webhook configured)
- Report any failures

### Dependency Injection for Testing
The `main()` function supports dependency injection for easier testing:
```python
from monitor.cpu import CpuMonitor
from monitor.memory import MemoryMonitor

# Create mock monitors
mock_cpu = MockCpuMonitor()
mock_memory = MockMemoryMonitor()

# Pass to main() for testing
main(cpu_monitor=mock_cpu, memory_monitor=mock_memory)
```

## 🐛 Troubleshooting

### Common Issues

**qBittorrent connection fails:**
- Ensure qBittorrent Web UI is enabled
- Check `QBITTORRENT_HOST`, `QBITTORRENT_PORT`, and `QBITTORRENT_PASSWORD`
- Verify `QBITTORRENT_MONITOR=true` is set

**Discord webhook not working:**
- Verify `WEBHOOK` environment variable is set correctly
- Check webhook URL is valid
- Ensure bot has permission to send messages

**Temperature monitoring not working:**
- Verify sensors are available (Linux only)
- Check `lm-sensors` is installed and configured
- Some systems may not expose temperature sensors

**Web dashboard not accessible:**
- Check `WEB_PORT` is not blocked by firewall
- Verify port is not already in use
- Check logs for server startup errors

**Systemd service fails with "Permission denied" (CHDIR error):**
- The installer will automatically detect permission issues
- When prompted, answer 'y' to fix permissions automatically
- Or manually fix with: `sudo chown -R jay:jay /path/to/serverMonitor`
- The service uses smart directory selection based on user permissions
- For non-root users, it will use the script directory or user's home directory

**Discord webhook rate limiting:**
- The system automatically rate limits Discord calls (30 per 60 seconds)
- If rate limited, alerts are logged to fallback file (see `ALERT_LOG_PATH`)
- Check `/var/log/server-monitor-alerts.log` for alerts that couldn't be sent

**Health check endpoint:**
- Access `http://localhost:8080/health` to check if the monitor is running
- Returns JSON with status and timestamp
- Useful for external monitoring tools

**Disk cleanup not working:**
- Ensure the monitor has write permissions to `/tmp`, `/var/tmp`, and cache directories
- Check logs for cleanup operation results
- Verify `DISK_CLEANUP_THRESHOLD` and `DISK_EMERGENCY_THRESHOLD` are set correctly
- Emergency cleanup triggers at 95% usage by default

**Kernel monitoring not detecting issues:**
- Verify system has access to `/var/log/kern.log`, `journalctl`, and `dmesg`
- Check that the monitor has permission to read kernel logs
- Some systems may not expose all kernel log sources
- Stability checks run more frequently (every 10 iterations) than crash checks (every 30)

## 📚 Dependencies

- `psutil` - System and process utilities
- `python-dotenv` - Environment variable management
- `requests` - HTTP requests for Discord webhooks
- `qbittorrent-api` - qBittorrent Web API client

## 🔄 Updates and Maintenance

The codebase follows clean code principles:
- No duplicate code
- Consistent error handling
- Standardized logging
- Type hints where applicable
- Input validation
- Thread-safe operations
- Constants instead of magic numbers
- Helper functions for repetitive operations
- Proper package structure with `__init__.py` files
- Dependency injection support for testing
- Rate limiting for external API calls
- Comprehensive cleanup in finally blocks

### Recent Improvements (2026)

**New Features:**
- **Kernel crash monitoring** - Proactive detection of kernel crashes, panics, and system instability
- **Automatic disk cleanup** - Prevents disk from reaching 100% usage with proactive cleanup
- **Separate webhook routing** - Dedicated webhooks for disk, crash, and qBittorrent alerts
- **System stability risk assessment** - Risk scoring with early warning detection
- **Unexpected reboot detection** - Tracks and alerts on unexpected system reboots

**Code Quality & Bug Fixes:**
- Fixed IndexError issues in kernel monitoring (added bounds checking)
- Fixed potential division by zero errors
- Fixed path slicing edge cases with safe `os.path.relpath()`
- Fixed timeout validation edge cases
- Simplified redundant datetime logic
- Moved inline imports to module level for better performance
- Comprehensive code review and bug fixes applied
- All critical and medium priority issues resolved

**Existing Improvements:**
- Fixed duplicate qBittorrent monitoring code
- Added null checks for optional monitors
- Implemented cached environment variable access
- Standardized exception handling across all modules
- Added rate limiting for Discord webhooks
- Implemented fallback alert logging
- Added health check endpoint
- Improved threshold validation with helper functions
- Added dependency injection to main() for testing
- Standardized boolean function naming (is_* prefix)
- Enhanced security with environment variable validation
- Optimized process list caching to reduce redundant iterations
- Added proper package structure with __init__.py files

## 📄 License

See project root for license information.

