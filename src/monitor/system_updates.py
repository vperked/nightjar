"""
Periodic OS package updates (Linux): refresh indexes, report pending upgrades, optional install.
"""

import os
import platform
import re
import shutil
import subprocess
from typing import Any, Dict, List, Optional

from logger import get_logger

logger = get_logger()


def _privilege_prefix() -> Optional[List[str]]:
    """Return [] to run commands directly, ['sudo','-n'] if passwordless sudo works, or None if not elevated."""
    if platform.system() != 'Linux':
        return None
    if os.geteuid() == 0:
        return []
    try:
        r = subprocess.run(
            ['sudo', '-n', 'true'],
            capture_output=True,
            timeout=10,
            check=False,
        )
        if r.returncode == 0:
            return ['sudo', '-n']
    except (FileNotFoundError, subprocess.TimeoutExpired, OSError):
        pass
    return None


def _which(name: str) -> Optional[str]:
    return shutil.which(name)


def _run(cmd: List[str], timeout: int, env: Optional[Dict[str, str]] = None) -> subprocess.CompletedProcess:
    e = {**os.environ}
    if env:
        e.update(env)
    return subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
        check=False,
        env=e,
    )


def _apt_upgradable_count(simulation_stdout: str) -> int:
    m = re.search(r'(\d+)\s+upgraded', simulation_stdout)
    if m:
        return int(m.group(1))
    return 0


def _detect_manager() -> Optional[str]:
    if _which('apt-get'):
        return 'apt'
    if _which('dnf'):
        return 'dnf'
    if _which('yum'):
        return 'yum'
    if _which('pacman'):
        return 'pacman'
    return None


def run_system_update(apply: bool, command_timeout: int) -> Dict[str, Any]:
    """
    Refresh package indexes, count pending upgrades, and optionally apply non-interactive upgrades.

    Args:
        apply: If True, install upgrades when available (package manager specific).
        command_timeout: Per-invocation subprocess timeout in seconds.

    Returns:
        Dict with ok, manager, upgradable, applied, message keys.
    """
    result: Dict[str, Any] = {
        'ok': False,
        'manager': 'none',
        'upgradable': 0,
        'applied': False,
        'message': 'skipped',
    }

    if platform.system() != 'Linux':
        result['message'] = 'not Linux'
        return result

    prefix = _privilege_prefix()
    if prefix is None:
        result['message'] = 'need root or passwordless sudo for package management'
        logger.warning('[SYSTEM_UPDATE] ' + result['message'])
        return result

    manager = _detect_manager()
    if not manager:
        result['message'] = 'no supported package manager (apt-get, dnf, yum, pacman)'
        logger.warning('[SYSTEM_UPDATE] ' + result['message'])
        return result

    result['manager'] = manager

    try:
        if manager == 'apt':
            env = {'DEBIAN_FRONTEND': 'noninteractive'}
            u = _run(prefix + ['apt-get', 'update', '-qq'], timeout=min(command_timeout, 600), env=env)
            if u.returncode != 0:
                err = (u.stderr or u.stdout or '')[:500]
                result['message'] = f'apt-get update failed ({u.returncode}): {err}'
                return result

            s = _run(prefix + ['apt-get', '-s', 'upgrade'], timeout=min(command_timeout, 600), env=env)
            upgradable = _apt_upgradable_count(s.stdout or '')
            result['upgradable'] = upgradable

            if apply and upgradable > 0:
                up = _run(
                    prefix
                    + [
                        'apt-get',
                        '-y',
                        '-o',
                        'Dpkg::Options::=--force-confdef',
                        '-o',
                        'Dpkg::Options::=--force-confold',
                        'upgrade',
                    ],
                    timeout=command_timeout,
                    env=env,
                )
                result['applied'] = up.returncode == 0
                if up.returncode != 0:
                    tail = (up.stderr or up.stdout or '')[:800]
                    result['message'] = f'apt-get upgrade failed ({up.returncode}): {tail}'
                    return result
                result['ok'] = True
                result['message'] = f'applied {upgradable} package upgrade(s) via apt'
            else:
                result['ok'] = True
                result['message'] = f'apt: {upgradable} package(s) pending' + (' (apply disabled)' if not apply else '')

        elif manager == 'dnf':
            chk = _run(prefix + ['dnf', '-q', 'check-update'], timeout=min(command_timeout, 600))
            upgradable = 0
            if chk.returncode == 0:
                upgradable = 0
            elif chk.returncode == 100:
                lines = [ln for ln in (chk.stdout or '').splitlines() if ln.strip() and not ln.startswith('Last metadata')]
                upgradable = len(lines)
            else:
                tail = (chk.stderr or chk.stdout or '')[:500]
                result['message'] = f'dnf check-update failed ({chk.returncode}): {tail}'
                return result
            result['upgradable'] = upgradable

            if apply and upgradable > 0:
                up = _run(prefix + ['dnf', '-y', 'upgrade'], timeout=command_timeout)
                result['applied'] = up.returncode == 0
                if up.returncode != 0:
                    tail = (up.stderr or up.stdout or '')[:800]
                    result['message'] = f'dnf upgrade failed ({up.returncode}): {tail}'
                    return result
                result['ok'] = True
                result['message'] = f'applied upgrades via dnf ({upgradable} packages reported)'
            else:
                result['ok'] = True
                result['message'] = f'dnf: {upgradable} package(s) pending' + (' (apply disabled)' if not apply else '')

        elif manager == 'yum':
            chk = _run(prefix + ['yum', '-q', 'check-update'], timeout=min(command_timeout, 600))
            upgradable = 0
            if chk.returncode == 0:
                upgradable = 0
            elif chk.returncode == 100:
                lines = [ln for ln in (chk.stdout or '').splitlines() if ln.strip()]
                upgradable = len(lines)
            else:
                tail = (chk.stderr or chk.stdout or '')[:500]
                result['message'] = f'yum check-update failed ({chk.returncode}): {tail}'
                return result
            result['upgradable'] = upgradable

            if apply and upgradable > 0:
                up = _run(prefix + ['yum', '-y', 'upgrade'], timeout=command_timeout)
                result['applied'] = up.returncode == 0
                if up.returncode != 0:
                    tail = (up.stderr or up.stdout or '')[:800]
                    result['message'] = f'yum upgrade failed ({up.returncode}): {tail}'
                    return result
                result['ok'] = True
                result['message'] = f'applied upgrades via yum ({upgradable} packages reported)'
            else:
                result['ok'] = True
                result['message'] = f'yum: {upgradable} package(s) pending' + (' (apply disabled)' if not apply else '')

        elif manager == 'pacman':
            sy = _run(prefix + ['pacman', '-Sy', '--noconfirm'], timeout=min(command_timeout, 600))
            if sy.returncode != 0:
                tail = (sy.stderr or sy.stdout or '')[:500]
                result['message'] = f'pacman -Sy failed ({sy.returncode}): {tail}'
                return result

            qu = _run(prefix + ['pacman', '-Qu'], timeout=min(command_timeout, 600))
            if qu.returncode == 1:
                upgradable = 0
            elif qu.returncode == 0:
                lines = [ln for ln in (qu.stdout or '').splitlines() if ln.strip()]
                upgradable = len(lines)
            else:
                tail = (qu.stderr or qu.stdout or '')[:500]
                result['message'] = f'pacman -Qu failed ({qu.returncode}): {tail}'
                return result
            result['upgradable'] = upgradable

            if apply and upgradable > 0:
                up = _run(prefix + ['pacman', '-Su', '--noconfirm'], timeout=command_timeout)
                result['applied'] = up.returncode == 0
                if up.returncode != 0:
                    tail = (up.stderr or up.stdout or '')[:800]
                    result['message'] = f'pacman -Su failed ({up.returncode}): {tail}'
                    return result
                result['ok'] = True
                result['message'] = f'applied {upgradable} package upgrade(s) via pacman'
            else:
                result['ok'] = True
                result['message'] = f'pacman: {upgradable} package(s) pending' + (' (apply disabled)' if not apply else '')

    except subprocess.TimeoutExpired:
        result['message'] = f'package manager timed out after {command_timeout}s'
        logger.warning('[SYSTEM_UPDATE] ' + result['message'])
        return result
    except Exception as ex:
        result['message'] = str(ex)
        logger.warning(f'[SYSTEM_UPDATE] error: {ex}')
        return result

    return result
