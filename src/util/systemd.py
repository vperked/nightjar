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
        Path to Python executable (absolute path)
    """
    python_exec = sys.executable
    if not os.path.isabs(python_exec):
        python_exec = os.path.abspath(python_exec)
    return python_exec


def get_main_script_path() -> Path:
    """
    Get the path to the main.py script.
    
    Returns:
        Path to main.py (absolute path)
    """
    project_root = get_project_root()
    main_script = project_root / 'src' / 'main.py'
    return main_script.resolve()


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
    
    python_path = str(python_exec).replace('\\', '/')
    main_script_path = str(main_script.resolve()).replace('\\', '/')
    working_dir_path = str(working_dir.resolve()).replace('\\', '/')
    
    if not os.path.exists(python_path):
        logger.error(f"Python executable not found: {python_path}")
        raise FileNotFoundError(f"Python executable not found: {python_path}")
    
    if not os.path.exists(main_script_path):
        logger.error(f"Main script not found: {main_script_path}")
        raise FileNotFoundError(f"Main script not found: {main_script_path}")
    
    if not os.path.exists(working_dir_path):
        logger.warning(f"Working directory does not exist: {working_dir_path}")
        working_dir_path = str(main_script.parent.resolve()).replace('\\', '/')
        logger.info(f"Using script directory as working directory: {working_dir_path}")
    elif not os.access(working_dir_path, os.R_OK | os.X_OK):
        logger.warning(f"Working directory not accessible: {working_dir_path}")
        working_dir_path = str(main_script.parent.resolve()).replace('\\', '/')
        logger.info(f"Using script directory as working directory: {working_dir_path}")
    
    if not os.path.exists(working_dir_path) or not os.access(working_dir_path, os.R_OK | os.X_OK):
        logger.warning(f"Script directory also not accessible, using /tmp as fallback")
        working_dir_path = '/tmp'
    
    venv_bin = str(project_root / 'venv' / 'bin').replace('\\', '/')
    if os.path.exists(venv_bin):
        minimal_path = f"{venv_bin}:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
    else:
        minimal_path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
    
    service_content = f"""[Unit]
Description=Server Monitor - System resource monitoring with Discord alerts
After=network.target

[Service]
Type=simple
User={service_user}
Group={service_user}
WorkingDirectory={working_dir_path}
Environment="PATH={minimal_path}\""""
    
    if env_file and os.path.exists(env_file):
        service_content += f"\nEnvironmentFile={env_file}"
    elif env_file:
        logger.warning(f"EnvironmentFile specified ({env_file}) but file does not exist. Skipping EnvironmentFile line.")
    
    service_content += f"""
ExecStart={python_path} {main_script_path}
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
        
        # Check if service already exists
        service_exists = service_file.exists()
        service_running = False
        
        if service_exists:
            # Check if service is currently running
            result = subprocess.run(
                ['systemctl', 'is-active', f'{service_name}.service'],
                capture_output=True,
                text=True,
                check=False
            )
            service_running = result.returncode == 0 and result.stdout.strip() == 'active'
            
            if service_running:
                logger.info(f"Service {service_name} is currently running. Stopping it...")
                stop_result = subprocess.run(
                    ['systemctl', 'stop', f'{service_name}.service'],
                    capture_output=True,
                    text=True,
                    check=False
                )
                if stop_result.returncode != 0:
                    logger.warning(f"Failed to stop existing service: {stop_result.stderr}")
                    if not force:
                        logger.warning("Use force=True to overwrite anyway.")
                        return False
                else:
                    logger.info("Service stopped successfully.")
            
            if not force:
                logger.info(f"Service file {service_file} already exists. Replacing it...")
        
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
        
        start_action = "Restarting" if service_running else "Starting"
        logger.info(f"{start_action} {service_name} service...")
        result = subprocess.run(
            ['systemctl', 'start', f'{service_name}.service'],
            capture_output=True,
            text=True,
            check=False
        )
        
        if result.returncode != 0:
            logger.error(f"Failed to start service: {result.stderr}")
            logger.error("Service file created but service failed to start.")
            logger.error("Please check the service file and logs:")
            logger.error(f"  cat /etc/systemd/system/{service_name}.service")
            logger.error(f"  sudo journalctl -u {service_name} -n 50")
            return False
        
        import time
        time.sleep(2)
        
        logger.info(f"Checking {service_name} service status...")
        result = subprocess.run(
            ['systemctl', 'is-active', f'{service_name}.service'],
            capture_output=True,
            text=True,
            check=False
        )
        
        if result.returncode != 0 or result.stdout.strip() != 'active':
            logger.error(f"Service is not active. Status: {result.stdout.strip()}")
            logger.error("Please check the logs:")
            logger.error(f"  sudo journalctl -u {service_name} -n 50")
            logger.error(f"  sudo systemctl status {service_name}")
            return False
        
        logger.info(f"\nService installed and started successfully!")
        logger.info(f"Service is running and will start automatically on system boot.")
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

