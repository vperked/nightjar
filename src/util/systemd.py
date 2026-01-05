"""
Systemd service installation utility.
Handles creating and installing systemd service files for the server monitor.
"""

import os
import sys
import subprocess
import shutil
import platform
from pathlib import Path
from logger import get_logger

logger = get_logger('Systemd')

# Systemd is only available on Linux
IS_LINUX = platform.system() == 'Linux'


def get_project_root() -> Path:
    """
    Get the project root directory.
    
    Returns:
        Path to the project root directory
    """
    current_file = Path(__file__).resolve()
    project_root = current_file.parent.parent.parent
    return project_root


def get_python_executable() -> str:
    """
    Get the path to the Python executable.
    
    Returns:
        Path to Python executable
    """
    return sys.executable


def get_main_script_path() -> Path:
    """
    Get the path to the main.py script.
    
    Returns:
        Path to main.py
    """
    project_root = get_project_root()
    return project_root / 'src' / 'main.py'


def is_root() -> bool:
    """
    Check if the script is running as root.
    
    Returns:
        True if running as root, False otherwise
    """
    if not IS_LINUX:
        return False
    return os.geteuid() == 0 if hasattr(os, 'geteuid') else False


def get_systemd_service_content(service_name: str = 'server-monitor') -> str:
    """
    Generate systemd service file content.
    
    Args:
        service_name: Name of the service
        
    Returns:
        Systemd service file content as string
    """
    project_root = get_project_root()
    python_exec = get_python_executable()
    main_script = get_main_script_path()
    working_dir = project_root
    
    if is_root():
        service_user = os.environ.get('SUDO_USER', 'root')
    else:
        service_user = os.environ.get('USER', 'root')
    
    if service_user != 'root':
        try:
            import pwd
            user_info = pwd.getpwnam(service_user)
            home_dir = user_info.pw_dir
        except (ImportError, KeyError):
            home_dir = os.path.expanduser(f'~{service_user}')
            if not os.path.exists(home_dir):
                home_dir = os.environ.get('HOME', f'/home/{service_user}')
    else:
        home_dir = '/root'
    
    env_file = None
    env_file_candidates = [
        os.path.join(home_dir, '.env'),
        str(project_root / '.env'),
        str(project_root / 'src' / '.env'),
    ]
    
    for candidate in env_file_candidates:
        if os.path.exists(candidate):
            env_file = candidate
            logger.info(f"Found .env file at: {env_file}")
            break
    
    if not env_file:
        logger.warning("No .env file found in any of the checked locations:")
        for candidate in env_file_candidates:
            logger.warning(f"  - {candidate}")
    
    service_content = f"""[Unit]
Description=Server Monitor - System resource monitoring with Discord alerts
After=network.target

[Service]
Type=simple
User={service_user}
Group={service_user}
WorkingDirectory={working_dir}
Environment="PATH={os.environ.get('PATH', '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin')}"""
    
    if env_file:
        service_content += f"\nEnvironmentFile={env_file}"
    else:
        logger.warning("No .env file found. Service will run without environment file.")
        logger.info("You may need to set environment variables directly in the service file or create a .env file.")
    
    service_content += f"""
ExecStart={python_exec} {main_script}
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

# Security settings
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
"""
    return service_content


def install_systemd_service(service_name: str = 'server-monitor', 
                           force: bool = False) -> bool:
    """
    Install the systemd service file.
    
    Args:
        service_name: Name of the service
        force: If True, overwrite existing service file
        
    Returns:
        True if installation was successful, False otherwise
    """
    if not IS_LINUX:
        logger.error("Systemd is only available on Linux systems.")
        return False
    
    if not is_root():
        logger.warning("Systemd service installation requires root privileges.")
        logger.info("Please run with sudo to install the systemd service.")
        return False
    
    try:
        project_root = get_project_root()
        python_exec = get_python_executable()
        main_script = get_main_script_path()
        
        logger.info(f"Project root: {project_root}")
        logger.info(f"Python executable: {python_exec}")
        logger.info(f"Main script: {main_script}")
        
        if not main_script.exists():
            logger.error(f"Main script not found at: {main_script}")
            logger.error("Please ensure you're running from the correct directory.")
            return False
        
        if not os.path.exists(python_exec):
            logger.error(f"Python executable not found at: {python_exec}")
            return False
        
        service_content = get_systemd_service_content(service_name)
        
        service_file = Path(f'/etc/systemd/system/{service_name}.service')
        
        if service_file.exists() and not force:
            logger.warning(f"Service file {service_file} already exists.")
            logger.info("Use force=True to overwrite, or remove the existing service first.")
            return False
        
        logger.info(f"Writing systemd service file to {service_file}...")
        service_file.write_text(service_content)
        logger.info(f"Service file written successfully.")
        
        logger.debug("Generated service file content:")
        logger.debug(service_content)
        
        logger.info("Reloading systemd daemon...")
        result = subprocess.run(
            ['systemctl', 'daemon-reload'],
            capture_output=True,
            text=True,
            check=False
        )
        
        if result.returncode != 0:
            logger.error(f"Failed to reload systemd daemon: {result.stderr}")
            return False
        
        logger.info("Systemd daemon reloaded successfully.")
        
        logger.info(f"Enabling {service_name} service...")
        result = subprocess.run(
            ['systemctl', 'enable', f'{service_name}.service'],
            capture_output=True,
            text=True,
            check=False
        )
        
        if result.returncode != 0:
            logger.warning(f"Failed to enable service: {result.stderr}")
            logger.info("You can enable it manually with: sudo systemctl enable server-monitor")
        else:
            logger.info(f"Service {service_name} enabled successfully.")
        
        logger.info(f"\nService installed successfully!")
        logger.info(f"To start the service: sudo systemctl start {service_name}")
        logger.info(f"To check status: sudo systemctl status {service_name}")
        logger.info(f"To view logs: sudo journalctl -u {service_name} -f")
        
        return True
        
    except PermissionError:
        logger.error("Permission denied. Please run with sudo.")
        return False
    except Exception as e:
        logger.error(f"Error installing systemd service: {e}")
        return False


def uninstall_systemd_service(service_name: str = 'server-monitor') -> bool:
    """
    Uninstall the systemd service.
    
    Args:
        service_name: Name of the service
        
    Returns:
        True if uninstallation was successful, False otherwise
    """
    if not IS_LINUX:
        logger.error("Systemd is only available on Linux systems.")
        return False
    
    if not is_root():
        logger.warning("Systemd service uninstallation requires root privileges.")
        logger.info("Please run with sudo to uninstall the systemd service.")
        return False
    
    try:
        service_file = Path(f'/etc/systemd/system/{service_name}.service')
        
        logger.info(f"Stopping {service_name} service...")
        subprocess.run(
            ['systemctl', 'stop', f'{service_name}.service'],
            capture_output=True,
            check=False
        )
        
        logger.info(f"Disabling {service_name} service...")
        subprocess.run(
            ['systemctl', 'disable', f'{service_name}.service'],
            capture_output=True,
            check=False
        )
        
        if service_file.exists():
            logger.info(f"Removing service file {service_file}...")
            service_file.unlink()
            logger.info("Service file removed.")
        
        logger.info("Reloading systemd daemon...")
        subprocess.run(
            ['systemctl', 'daemon-reload'],
            capture_output=True,
            check=False
        )
        
        logger.info(f"Service {service_name} uninstalled successfully.")
        return True
        
    except PermissionError:
        logger.error("Permission denied. Please run with sudo.")
        return False
    except Exception as e:
        logger.error(f"Error uninstalling systemd service: {e}")
        return False


def check_systemd_available() -> bool:
    """
    Check if systemd is available on the system.
    
    Returns:
        True if systemd is available, False otherwise
    """
    if not IS_LINUX:
        return False
    
    if not shutil.which('systemctl'):
        return False
    
    try:
        result = subprocess.run(
            ['systemctl', '--version'],
            capture_output=True,
            text=True,
            check=False
        )
        return result.returncode == 0
    except FileNotFoundError:
        return False

