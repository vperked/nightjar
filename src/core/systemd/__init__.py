"""
systemd service installation and management.
"""

from core.systemd.service import (
    IS_LINUX,
    STOP_TIMEOUT,
    KILL_WAIT_TIMEOUT,
    REQUIRED_MODULES,
    get_project_root,
    get_python_executable,
    get_main_script_path,
    check_dependencies,
    get_service_state,
    stop_service,
    force_kill_service,
    get_unkillable_processes,
    is_root,
    check_directory_access,
    fix_directory_permissions,
    get_systemd_service_content,
    install_systemd_service,
    uninstall_systemd_service,
    is_systemd_available,
)

__all__ = [
    'IS_LINUX',
    'KILL_WAIT_TIMEOUT',
    'STOP_TIMEOUT',
    'REQUIRED_MODULES',
    'check_dependencies',
    'force_kill_service',
    'get_unkillable_processes',
    'get_service_state',
    'stop_service',
    'check_directory_access',
    'fix_directory_permissions',
    'get_main_script_path',
    'get_project_root',
    'get_python_executable',
    'get_systemd_service_content',
    'install_systemd_service',
    'is_root',
    'is_systemd_available',
    'uninstall_systemd_service',
]
