"""
Server Monitor entrypoint.

CLI flags:
  --test              Run monitor/webhook self-test and exit
  --install-systemd   Install the systemd service and exit
  --force             Force systemd reinstall when used with --install-systemd
"""

import sys

from core.config import get_cached_env, get_env_bool
from core.observability import Component, get_logger
from core.runtime import run_monitor
from core.selfTest import run_self_test
from core.systemd import install_systemd_service, is_systemd_available

logger = get_logger(Component.CORE)


def main(*args, **kwargs):
    """
    CLI entrypoint: optional self-test or systemd install, then run monitoring.

    Extra positional/keyword arguments are forwarded to run_monitor for tests
    that inject monitor instances.
    """
    if '--test' in sys.argv:
        ok = run_self_test()
        sys.exit(0 if ok else 1)

    install_systemd = get_env_bool('INSTALL_SYSTEMD', False)
    if '--install-systemd' in sys.argv or install_systemd:
        logger.info("Systemd installation requested...")
        if is_systemd_available():
            service_name = get_cached_env('SYSTEMD_SERVICE_NAME', 'server-monitor')
            force = '--force' in sys.argv or get_env_bool('SYSTEMD_FORCE_INSTALL', False)
            success = install_systemd_service(service_name=service_name, force=force)
            if success:
                logger.info("Systemd service installed successfully!")
                logger.info("The service will start automatically on system boot.")
                logger.info("You can start it now with: sudo systemctl start server-monitor")
                logger.info("To check status: sudo systemctl status server-monitor")
                logger.info("To view logs: sudo journalctl -u server-monitor -f")
                sys.exit(0)
            else:
                logger.error("Failed to install systemd service.")
                logger.info("Make sure you're running with sudo: sudo python src/main.py --install-systemd")
                sys.exit(1)
        else:
            logger.error("Systemd is not available on this system.")
            logger.info("Systemd installation is only supported on Linux systems with systemd.")
            sys.exit(1)

    run_monitor(*args, **kwargs)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
