"""
Systemd service installation utility.
Handles creating and installing systemd service files for the server monitor.
"""

import os
import sys
import subprocess
import shutil
import platform
import time
from pathlib import Path
from core.observability import Component, get_logger

logger = get_logger(Component.SYSTEMD)

IS_LINUX = platform.system() == 'Linux'


def get_project_root() -> Path:
    """
    Get the project root, the directory that contains src/main.py.

    Located by walking up from this file rather than by a fixed number of parent
    hops, so moving this module between packages cannot silently point the
    generated unit file at the wrong tree.

    Returns:
        Path to the project root directory
    """
    for candidate in Path(__file__).resolve().parents:
        if (candidate / 'src' / 'main.py').is_file():
            return candidate
    return Path(__file__).resolve().parents[3]


def get_python_executable() -> str:
    """
    Get the Python interpreter the service should run under.

    Prefers the project's virtualenv over the running interpreter. systemd starts
    the service with a clean environment and no venv activation, so ExecStart must
    name an interpreter that actually has the dependencies installed. sys.executable
    is wrong whenever the installer is invoked as 'sudo python3 ...', because sudo
    resets PATH and picks the system interpreter.

    The venv path is deliberately not symlink-resolved: venv/bin/python3 usually
    links to the system interpreter, and Python locates pyvenv.cfg relative to the
    path it was invoked by. Resolving it would silently drop the venv's packages.

    Returns:
        Absolute path to the Python executable
    """
    venv_python = get_project_root() / 'venv' / 'bin' / 'python3'
    if venv_python.is_file() and os.access(venv_python, os.X_OK):
        return str(venv_python)

    python_exec = Path(sys.executable)
    if not python_exec.is_absolute():
        python_exec = python_exec.resolve()
    return str(python_exec)


def get_main_script_path() -> Path:
    """
    Get the path to the main.py script.

    Returns:
        Path to main.py (absolute path)
    """
    project_root = get_project_root()
    main_script = project_root / 'src' / 'main.py'
    return main_script.resolve()


REQUIRED_MODULES = ('psutil', 'dotenv', 'requests')

STOP_TIMEOUT = 30.0
KILL_WAIT_TIMEOUT = 20.0


def get_service_state(service_name: str = 'server-monitor') -> str:
    """
    Get the current systemd state of the service.

    Args:
        service_name: Name of the service

    Returns:
        State reported by systemctl, or 'unknown' if it cannot be determined
    """
    try:
        result = subprocess.run(
            ['systemctl', 'is-active', f'{service_name}.service'],
            capture_output=True,
            text=True,
            check=False,
            timeout=10,
        )
    except (subprocess.TimeoutExpired, FileNotFoundError, OSError):
        return 'unknown'
    return result.stdout.strip() or 'unknown'


def force_kill_service(service_name: str = 'server-monitor',
                       timeout: float = KILL_WAIT_TIMEOUT) -> bool:
    """
    SIGKILL every process in the service's control group and wait for it to clear.

    Used when a graceful stop hangs, which happens when a process ignores SIGTERM
    or an orphaned instance is left in the control group from an earlier run.

    Args:
        service_name: Name of the service
        timeout: Seconds to wait for the unit to leave the active states

    Returns:
        True if the service is no longer active
    """
    logger.warning(f"Force killing {service_name} (SIGKILL to the whole control group)...")

    try:
        subprocess.run(
            ['systemctl', 'kill', '--signal=SIGKILL', '--kill-whom=all', f'{service_name}.service'],
            capture_output=True,
            text=True,
            check=False,
            timeout=15,
        )
    except (subprocess.TimeoutExpired, FileNotFoundError, OSError) as e:
        logger.error(f"Failed to send SIGKILL to {service_name}: {e}")

    deadline = time.time() + timeout
    while time.time() < deadline:
        state = get_service_state(service_name)
        if state not in ('active', 'activating', 'deactivating', 'reloading'):
            logger.info(f"Service {service_name} is now '{state}'.")
            subprocess.run(
                ['systemctl', 'reset-failed', f'{service_name}.service'],
                capture_output=True,
                check=False,
                timeout=10,
            )
            return True
        time.sleep(0.5)

    unkillable = get_unkillable_processes(service_name)
    if unkillable:
        logger.error(
            f"Service {service_name} cannot be stopped: "
            f"{len(unkillable)} process(es) are in uninterruptible kernel sleep (D state)."
        )
        for pid, wchan, cmd in unkillable:
            logger.error(f"  PID {pid} blocked in '{wchan}': {cmd}")
        logger.error(
            "SIGKILL cannot terminate a D-state process; it exits only when the kernel "
            "call returns. This is usually a stuck driver (for example GPU or disk I/O). "
            "The unit will stay in 'deactivating' until then, and a reboot is normally "
            "the only way to clear it."
        )
    else:
        logger.error(
            f"Service {service_name} is still '{get_service_state(service_name)}' after SIGKILL. "
            f"Check: systemctl status {service_name}"
        )
    return False


def get_unkillable_processes(service_name: str = 'server-monitor') -> list:
    """
    Find processes in the service's control group stuck in uninterruptible sleep.

    A process in D state ignores every signal, including SIGKILL, until the kernel
    call it is blocked in returns. systemd will hold the unit in 'deactivating'
    for as long as one remains in the control group.

    Args:
        service_name: Name of the service

    Returns:
        List of (pid, wchan, cmdline) tuples for D-state processes
    """
    stuck = []
    cgroup_procs = Path(f'/sys/fs/cgroup/system.slice/{service_name}.service/cgroup.procs')

    try:
        pids = cgroup_procs.read_text().split()
    except OSError:
        return stuck

    for pid in pids:
        try:
            state_line = next(
                line for line in Path(f'/proc/{pid}/status').read_text().splitlines()
                if line.startswith('State:')
            )
            if '\tD' not in state_line and ' D ' not in state_line:
                continue
            wchan = Path(f'/proc/{pid}/wchan').read_text().strip() or 'unknown'
            cmd = Path(f'/proc/{pid}/cmdline').read_bytes().replace(b'\x00', b' ').decode().strip()
            stuck.append((pid, wchan, cmd))
        except (OSError, StopIteration):
            continue

    return stuck


def stop_service(service_name: str = 'server-monitor',
                 timeout: float = STOP_TIMEOUT) -> bool:
    """
    Stop the service, escalating to SIGKILL if it will not stop in time.

    Args:
        service_name: Name of the service
        timeout: Seconds to allow for a graceful stop before killing

    Returns:
        True if the service ended up stopped
    """
    logger.info(f"Stopping {service_name} service...")

    try:
        result = subprocess.run(
            ['systemctl', 'stop', f'{service_name}.service'],
            capture_output=True,
            text=True,
            check=False,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        logger.warning(
            f"'systemctl stop {service_name}' did not finish within {timeout:.0f}s; escalating to SIGKILL."
        )
        return force_kill_service(service_name)

    if result.returncode != 0:
        logger.warning(f"Failed to stop {service_name} gracefully: {result.stderr.strip()}")
        return force_kill_service(service_name)

    if get_service_state(service_name) in ('active', 'deactivating'):
        logger.warning(f"{service_name} is still shutting down after stop returned; escalating to SIGKILL.")
        return force_kill_service(service_name)

    logger.info("Service stopped successfully.")
    return True


def check_dependencies(python_exec: str) -> list:
    """
    Report which required modules the given interpreter cannot import.

    Args:
        python_exec: Path to the Python interpreter the service will use

    Returns:
        List of missing module names, empty when the interpreter is usable
    """
    missing = []
    for module in REQUIRED_MODULES:
        try:
            result = subprocess.run(
                [python_exec, '-c', f'import {module}'],
                capture_output=True,
                check=False,
                timeout=30,
            )
        except (subprocess.TimeoutExpired, FileNotFoundError, OSError):
            return list(REQUIRED_MODULES)
        if result.returncode != 0:
            missing.append(module)
    return missing


def is_root() -> bool:
    """
    Check if the script is running as root.

    Returns:
        True if running as root, False otherwise
    """
    if not IS_LINUX:
        return False
    return os.geteuid() == 0 if hasattr(os, 'geteuid') else False


def check_directory_access(directory: str, username: str) -> bool:
    """
    Check if a directory is accessible by a specific user.

    Args:
        directory: Directory path to check
        username: Username to check access for

    Returns:
        True if accessible, False otherwise
    """
    if not Path(directory).exists():
        return False

    if username == 'root':
        return os.access(directory, os.R_OK | os.X_OK)

    try:
        result = subprocess.run(
            ['su', '-s', '/bin/bash', '-c', f'test -r "{directory}" && test -x "{directory}"', username],
            capture_output=True,
            text=True,
            check=False,
            timeout=5
        )
        return result.returncode == 0
    except (subprocess.TimeoutExpired, FileNotFoundError):
        try:
            import pwd
            import stat
            user_info = pwd.getpwnam(username)
            dir_stat = os.stat(directory)
            dir_mode = stat.S_IMODE(dir_stat.st_mode)

            if dir_stat.st_uid == user_info.pw_uid:
                return bool(dir_mode & stat.S_IRUSR and dir_mode & stat.S_IXUSR)
            elif dir_stat.st_gid == user_info.pw_gid:
                return bool(dir_mode & stat.S_IRGRP and dir_mode & stat.S_IXGRP)
            else:
                return bool(dir_mode & stat.S_IROTH and dir_mode & stat.S_IXOTH)
        except (ImportError, KeyError, OSError):
            return False


def fix_directory_permissions(directory: str, username: str) -> bool:
    """
    Fix directory permissions so the specified user can access it.
    Also fixes parent directories to ensure the path is accessible.

    Args:
        directory: Directory path to fix
        username: Username that needs access

    Returns:
        True if permissions were fixed, False otherwise
    """
    try:
        import pwd
        user_info = pwd.getpwnam(username)
        uid = user_info.pw_uid
        gid = user_info.pw_gid
    except (ImportError, KeyError):
        logger.error(f"Could not get user info for {username}")
        return False

    try:
        path = Path(directory).resolve()

        parent_paths = []
        current = path
        while current != current.parent:
            parent_paths.append(current)
            current = current.parent
        parent_paths.reverse()

        for p in parent_paths:
            if not p.exists():
                continue

            try:
                current_stat = p.stat()

                if current_stat.st_uid != uid:
                    os.chown(p, uid, gid)
                    logger.info(f"Changed ownership of {p} to {username}")

                os.chmod(p, 0o755)
            except OSError as e:
                logger.warning(f"Could not fix permissions on {p}: {e}")
                if p == path:
                    return False

        logger.info(f"Fixed permissions on {directory} and parent directories")
        return True
    except OSError as e:
        logger.error(f"Failed to fix permissions on {directory}: {e}")
        return False


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
            if not Path(home_dir).exists():
                home_dir = os.environ.get('HOME', f'/home/{service_user}')
    else:
        home_dir = '/root'

    env_file = None
    env_file_candidates = [
        str(Path(home_dir) / '.env'),
        str(project_root / '.env'),
        str(project_root / 'src' / '.env'),
    ]

    for candidate in env_file_candidates:
        if Path(candidate).exists():
            env_file = candidate
            logger.info(f"Found .env file at: {env_file}")
            break

    if not env_file:
        logger.warning("No .env file found in any of the checked locations:")
        for candidate in env_file_candidates:
            logger.warning(f"  - {candidate}")

    python_path = str(python_exec).replace('\\', '/')
    main_script_path = str(main_script.resolve()).replace('\\', '/')

    if not Path(python_path).exists():
        logger.error(f"Python executable not found: {python_path}")
        raise FileNotFoundError(f"Python executable not found: {python_path}")

    if not Path(main_script_path).exists():
        logger.error(f"Main script not found: {main_script_path}")
        raise FileNotFoundError(f"Main script not found: {main_script_path}")

    script_dir = str(main_script.parent.resolve()).replace('\\', '/')
    project_root_path = str(project_root.resolve()).replace('\\', '/')

    if service_user == 'root':
        working_dir_path = project_root_path
        if not Path(working_dir_path).exists():
            logger.warning(f"Project root not accessible, using script directory: {script_dir}")
            working_dir_path = script_dir
    else:
        if check_directory_access(script_dir, service_user):
            working_dir_path = script_dir
            logger.info(f"Using script directory as working directory: {working_dir_path}")
        elif check_directory_access(project_root_path, service_user):
            working_dir_path = project_root_path
            logger.info(f"Using project root as working directory: {working_dir_path}")
        else:
            logger.warning(f"Service user '{service_user}' cannot access script directory or project root")
            try:
                import pwd
                user_info = pwd.getpwnam(service_user)
                home_dir = user_info.pw_dir
                working_dir_path = home_dir
                logger.info(f"Falling back to service user home directory: {working_dir_path}")
            except (ImportError, KeyError):
                working_dir_path = '/tmp'
                logger.warning("Could not determine home directory, using /tmp as fallback")

    logger.info(f"Working directory: {working_dir_path} (service user: {service_user})")

    venv_bin = str(project_root / 'venv' / 'bin').replace('\\', '/')
    if Path(venv_bin).exists():
        minimal_path = f"{venv_bin}:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
    else:
        minimal_path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

    service_content = f"""[Unit]
Description=Server Monitor - System resource monitoring with Discord alerts
After=network.target
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
User={service_user}
Group={service_user}
WorkingDirectory={working_dir_path}
Environment="PATH={minimal_path}\""""

    if env_file and Path(env_file).exists():
        service_content += f"\nEnvironmentFile={env_file}"
    elif env_file:
        logger.warning(f"EnvironmentFile specified ({env_file}) but file does not exist. Skipping EnvironmentFile line.")

    service_content += f"""
ExecStart={python_path} {main_script_path}
Restart=always
RestartSec=10
TimeoutStopSec=20
KillMode=control-group
KillSignal=SIGTERM
SendSIGKILL=yes
StandardOutput=journal
StandardError=journal

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

        if not Path(python_exec).exists():
            logger.error(f"Python executable not found at: {python_exec}")
            return False

        missing = check_dependencies(python_exec)
        if missing:
            logger.error(f"{python_exec} cannot import: {', '.join(missing)}")
            logger.error("systemd would start this interpreter and the service would crash-loop.")
            logger.error(f"Install the dependencies for it, or create the venv the service expects:")
            logger.error(f"  {python_exec} -m pip install -r {project_root / 'requirements.txt'}")
            return False

        if is_root():
            service_user = os.environ.get('SUDO_USER', 'root')
        else:
            service_user = os.environ.get('USER', 'root')

        main_script = get_main_script_path()
        script_dir = str(main_script.parent.resolve()).replace('\\', '/')
        project_root = get_project_root()

        if service_user == 'root':
            working_dir_path = str(project_root.resolve()).replace('\\', '/')
            if not Path(working_dir_path).exists():
                working_dir_path = script_dir
        else:
            working_dir_path = script_dir

        if service_user != 'root' and not check_directory_access(working_dir_path, service_user):
            logger.warning(f"Service user '{service_user}' does not have access to working directory: {working_dir_path}")
            logger.info("The service will fail to start without proper permissions.")

            try:
                response = input(f"Would you like to fix permissions on '{working_dir_path}' for user '{service_user}'? (y/n): ").strip().lower()
                if response in ('y', 'yes'):
                    if fix_directory_permissions(working_dir_path, service_user):
                        logger.info("Permissions fixed successfully!")
                    else:
                        logger.error("Failed to fix permissions. You may need to fix them manually.")
                        logger.info(f"Try: sudo chown -R {service_user}:{service_user} {working_dir_path}")
                        logger.info(f"      sudo chmod -R 755 {working_dir_path}")
                else:
                    logger.warning("Skipping permission fix. Service may fail to start.")
            except (EOFError, KeyboardInterrupt):
                logger.warning("Permission fix skipped. Service may fail to start.")

        service_content = get_systemd_service_content(service_name)

        service_file = Path(f'/etc/systemd/system/{service_name}.service')

        service_exists = service_file.exists()
        service_running = False

        if service_exists:
            state = get_service_state(service_name)
            service_running = state in ('active', 'activating', 'deactivating', 'reloading')

            if service_running:
                logger.info(f"Service {service_name} is currently '{state}'.")
                if not stop_service(service_name):
                    logger.error("Could not stop the existing service, even with SIGKILL.")
                    if not force:
                        logger.warning("Use force=True to overwrite anyway.")
                        return False

            if not force:
                logger.info(f"Service file {service_file} already exists. Replacing it...")

        logger.info(f"Writing systemd service file to {service_file}...")
        service_file.write_text(service_content)
        logger.info("Service file written successfully.")

        logger.debug("Generated service file content:")
        logger.debug(service_content)

        logger.info("Reloading systemd daemon...")
        result = subprocess.run(
            ['systemctl', 'daemon-reload'],
            capture_output=True,
            text=True,
            check=False,
            timeout=30
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
            check=False,
            timeout=10
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
            check=False,
            timeout=30
        )

        if result.returncode != 0:
            logger.error(f"Failed to start service: {result.stderr}")
            logger.error("Service file created but service failed to start.")
            logger.error("Please check the service file and logs:")
            logger.error(f"  cat /etc/systemd/system/{service_name}.service")
            logger.error(f"  sudo journalctl -u {service_name} -n 50")
            return False

        time.sleep(2)

        logger.info(f"Checking {service_name} service status...")
        state = get_service_state(service_name)

        if state != 'active':
            logger.error(f"Service is not active. Status: {state}")
            logger.error("Please check the logs:")
            logger.error(f"  sudo journalctl -u {service_name} -n 50")
            logger.error(f"  sudo systemctl status {service_name}")
            return False

        logger.info("\nService installed and started successfully!")
        logger.info("Service is running and will start automatically on system boot.")
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

        stop_service(service_name)

        logger.info(f"Disabling {service_name} service...")
        subprocess.run(
            ['systemctl', 'disable', f'{service_name}.service'],
            capture_output=True,
            check=False,
            timeout=10
        )

        if service_file.exists():
            logger.info(f"Removing service file {service_file}...")
            service_file.unlink()
            logger.info("Service file removed.")

        logger.info("Reloading systemd daemon...")
        subprocess.run(
            ['systemctl', 'daemon-reload'],
            capture_output=True,
            check=False,
            timeout=30
        )

        logger.info(f"Service {service_name} uninstalled successfully.")
        return True

    except PermissionError:
        logger.error("Permission denied. Please run with sudo.")
        return False
    except Exception as e:
        logger.error(f"Error uninstalling systemd service: {e}")
        return False


def is_systemd_available() -> bool:
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
            check=False,
            timeout=5
        )
        return result.returncode == 0
    except FileNotFoundError:
        return False

