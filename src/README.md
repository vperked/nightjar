# Server Monitor - Source Code Documentation

A comprehensive server monitoring system with Discord alerts, web dashboard, and system resource tracking.

## 📁 Directory Structure

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
│   ├── disk.py           # Disk usage and directory monitoring
│   ├── network.py        # Network bandwidth and I/O monitoring
│   ├── gpu.py            # NVIDIA GPU monitoring (nvidia-smi)
│   ├── sensors.py        # Temperature and fan speed monitoring
│   ├── system.py         # System info, processes, and uptime
│   ├── processes.py      # Process resource usage tracking
│   ├── freeze.py         # System freeze detection
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
- Disk usage alerts
- Network bandwidth alerts
- Temperature alerts (with cause analysis)
- Process resource alerts
- Directory size alerts
- qBittorrent notifications
- System freeze alerts

**Features:**
- Consistent embed styling (red color scheme)
- Detailed field information
- Timestamps and footers
- Error handling and retry logic

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
- `GET /js/main.js` - JavaScript frontend

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

#### `util/error_handling.py`
Standardized error handling utilities:
- Connection error decorators
- Logging helpers
- Error pattern standardization

#### `cache/cache.py`
Cache management:
- Startup cache clearing
- Exit cache cleanup
- Directory cache management

## ⚙️ Configuration

All configuration is done via environment variables (`.env` file):

### Required
- `WEBHOOK` - Discord webhook URL for alerts

### Optional Features
- `QBITTORRENT_MONITOR` - Enable qBittorrent monitoring (true/false)
- `MEDIA_MONITOR_ENABLED` - Enable media directory monitoring (true/false)
- `FREEZE_MONITOR_ENABLED` - Enable freeze detection (true/false)
- `SELF_TEST_ON_START` - Run self-test on startup (true/false, default: true)

### Thresholds
- `CPU_THRESHOLD` - CPU usage alert threshold (default: 80.0%)
- `MEMORY_THRESHOLD` - Memory usage threshold (default: 80.0%)
- `GPU_THRESHOLD` - GPU utilization threshold (default: 80.0%)
- `DISK_THRESHOLD` - Disk usage threshold (default: 80.0%)
- `TEMP_THRESHOLD` - Temperature threshold in Celsius (default: 80.0°C)
- `NETWORK_BANDWIDTH_THRESHOLD` - Network bandwidth in MB/s (default: 1000.0)
- `PROCESS_CPU_THRESHOLD` - Per-process CPU threshold (default: 50.0%)
- `PROCESS_MEMORY_THRESHOLD` - Per-process memory threshold (default: 10.0%)

### Timing
- `CHECK_INTERVAL` - Monitoring loop interval in seconds (default: 5.0)
- `ALERT_COOLDOWN` - Seconds between alerts for same metric (default: 300.0)
- `TEMP_ALERT_DURATION` - Seconds temp must stay high before alert (default: 30.0)

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
```

## 📊 Monitoring Features

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

## 🏗️ Architecture

### Design Patterns
- **Modular Monitors**: Each system component has its own monitor class
- **Observer Pattern**: Monitors collect data, main loop processes alerts
- **Singleton Pattern**: Web server uses global monitor instances
- **Factory Pattern**: Monitor initialization based on configuration

### Error Handling
- Specific exceptions for known error types
- Broad Exception catching for unexpected errors (with proper logging)
- Connection errors logged at debug level (expected transient)
- Critical errors logged at error level

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

## 📄 License

See project root for license information.

