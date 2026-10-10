"""
Kernel stability, crash and hardware-error alerts.
"""

import hashlib
import json
from datetime import datetime
from typing import Any

from core.config import (
    MAX_CRASH_TYPES_DISPLAY,
    MAX_HARDWARE_ERRORS_DISPLAY,
    MAX_INDICATORS_DISPLAY,
    MAX_RECOMMENDATIONS_DISPLAY,
)
from core.observability import Component, get_logger
from core.runtime.handlers.base import Handler
from core.runtime.snapshot import Snapshot


Field = dict[str, Any]


def _field(name: str, value: str, inline: bool = True) -> Field:
    return {'name': name, 'value': value, 'inline': inline}


def _format_count(count: int) -> str:
    if count >= 1_000_000:
        return f"{count / 1_000_000:.2f}M"
    if count >= 1000:
        return f"{count / 1000:.2f}K"
    return str(count)


def describe_hardware_error(hw_error: dict[str, Any]) -> str:
    """One Markdown line describing a hardware error reported by the kernel monitor."""
    source = hw_error.get('source', 'unknown')
    if hw_error.get('type') == 'high_interrupts':
        device = hw_error.get('device', 'unknown')
        count = _format_count(hw_error.get('count', 0))
        return f"**High Interrupts** on {device}: {count} interrupts - possible hardware issue"
    if 'count_increase' in hw_error:
        error_type = hw_error.get('error_type', 'unknown')
        return f"**{source}**: {hw_error['count_increase']} new {error_type} errors (total: {hw_error.get('count', 0)})"
    if 'count' in hw_error:
        error_type = hw_error.get('error_type', 'unknown')
        if 'device' in hw_error:
            return f"**{source}** ({hw_error['device']}): {hw_error['count']} {error_type} errors"
        return f"**{source}**: {hw_error['count']} {error_type} errors"
    if 'message' in hw_error:
        message = hw_error['message']
        if len(message) > 200:
            message = message[:197] + "..."
        return f"**{source}**: {message}"
    if 'type' in hw_error:
        return f"**{source}**: {hw_error['type']} error detected"
    return f"**{source}**: {hw_error.get('error_type', 'unknown')} error"


def _hardware_error_key(hw_error: dict[str, Any]) -> str:
    return f"{hw_error.get('type', 'unknown')}_{hw_error.get('device', '')}_{hw_error.get('source', '')}"


def _format_boot_time(timestamp) -> str:
    return datetime.fromtimestamp(timestamp).strftime('%Y-%m-%d %H:%M:%S') if timestamp else 'Unknown'


class KernelChecks(Handler):
    """Periodic kernel stability scoring and crash/early-warning detection."""

    log = get_logger(Component.KERNEL)

    def __init__(self, ctx):
        super().__init__(ctx)
        self.kernel_stability_check_counter = 0
        self.kernel_stability_alert_sent = False
        self.last_kernel_stability_alert_time = 0
        self.last_kernel_stability_alert_hash = None
        self.kernel_check_counter = 0
        self.kernel_alert_sent = False
        self.last_kernel_alert_time = 0
        self.last_hardware_error_alert_times: dict[str, float] = {}

    # ---- stability ---------------------------------------------------------------------------

    def check_stability(self, snap: Snapshot) -> None:
        """Alert when the stability risk score is high or critical."""
        cfg = self.cfg
        self.kernel_stability_check_counter += 1
        if not cfg.kernel_monitor_enabled or self.kernel_stability_check_counter < cfg.kernel_stability_check_interval:
            return
        self.kernel_stability_check_counter = 0
        try:
            stability = self.ctx.kernel_monitor.check_system_stability()
            if stability.get('risk_level') in ['high', 'critical']:
                now = snap.current_time
                if (
                    not self.kernel_stability_alert_sent
                    or now - self.last_kernel_stability_alert_time >= cfg.alert_cooldown
                ):
                    self._alert_stability(stability)
                    self.last_kernel_stability_alert_time = now
                    self.kernel_stability_alert_sent = True
            elif self.kernel_stability_alert_sent:
                self.kernel_stability_alert_sent = False
                self.last_kernel_stability_alert_hash = None
        except Exception as e:
            self.log.debug(f"Kernel stability check failed: {e}")

    def _alert_stability(self, stability: dict[str, Any]) -> None:
        ctx = self.ctx
        risk_level = stability.get('risk_level', 'unknown').upper()
        risk_score = stability.get('risk_score', 0)
        self.log.warning(f"[KERNEL] System stability risk: {risk_level} (score: {risk_score})")
        if not ctx.crash_webhook:
            return

        fields = [
            _field('Risk Level', risk_level),
            _field('Risk Score', str(risk_score)),
            _field('Status', 'System showing signs of instability - crash risk elevated', inline=False),
        ]
        indicators = stability.get('indicators', [])
        if indicators:
            fields.append(_field('Warning Indicators', '\n'.join(indicators[:MAX_INDICATORS_DISPLAY]), inline=False))
        recommendations = stability.get('recommendations', [])
        if recommendations:
            fields.append(
                _field('Recommended Actions', '\n'.join(recommendations[:MAX_RECOMMENDATIONS_DISPLAY]), inline=False)
            )

        title = f'Kernel Alert: System Stability Risk ({risk_level})'
        description = 'System showing signs of instability that may lead to kernel crash'
        # Identical content is only sent once, however long the risk persists.
        content = {
            'title': title,
            'description': description,
            'fields': sorted(fields, key=lambda f: (f['name'], f['value'])),
        }
        alert_hash = hashlib.md5(json.dumps(content, sort_keys=True).encode()).hexdigest()
        if alert_hash == self.last_kernel_stability_alert_hash:
            self.log.debug("Skipping duplicate kernel stability alert")
            return

        if ctx.deliver(
            ctx.crash_webhook,
            'send_embed',
            "crash_webhook send_embed",
            title=title,
            description=description,
            fields=fields,
            footer='Server Monitor - Kernel Stability',
            timestamp=True,
        ):
            self.log.info("    Discord kernel stability alert sent")
            self.last_kernel_stability_alert_hash = alert_hash
        else:
            self.log.warning("    Discord kernel stability alert failed")

    # ---- crashes, reboots and early warnings -------------------------------------------------

    def check_crashes(self, snap: Snapshot) -> None:
        """Alert on unexpected reboots, early warnings, hardware errors and crashes."""
        cfg = self.cfg
        self.kernel_check_counter += 1
        if not cfg.kernel_monitor_enabled or self.kernel_check_counter < cfg.kernel_check_interval:
            return
        self.kernel_check_counter = 0
        now = snap.current_time
        try:
            results = self.ctx.kernel_monitor.check_kernel_crashes(since_minutes=cfg.kernel_check_since_minutes)

            if results.get('unexpected_reboot') and self._kernel_alert_due(now):
                self._alert_reboot(results['unexpected_reboot'])
                self._mark_kernel_alert(now)

            critical_warnings = [w for w in results.get('early_warnings', []) if w.get('severity') == 'critical']
            hardware_errors = results.get('hardware_errors', [])
            if (critical_warnings and self._kernel_alert_due(now)) or self._hardware_alert_due(hardware_errors, now):
                self._alert_early_warnings(critical_warnings, hardware_errors, now)
                if critical_warnings:
                    self._mark_kernel_alert(now)

            crashes = results.get('crashes_found', [])
            if crashes:
                if self._kernel_alert_due(now):
                    self._alert_crashes(crashes)
                    self._mark_kernel_alert(now)
            elif self.kernel_alert_sent:
                self.kernel_alert_sent = False
        except Exception as e:
            self.log.debug(f"Kernel crash check failed: {e}")

    def _kernel_alert_due(self, now: float) -> bool:
        return not self.kernel_alert_sent or now - self.last_kernel_alert_time >= self.cfg.alert_cooldown

    def _mark_kernel_alert(self, now: float) -> None:
        self.last_kernel_alert_time = now
        self.kernel_alert_sent = True

    def _hardware_alert_due(self, hardware_errors: list[dict[str, Any]], now: float) -> bool:
        """Critical hardware errors repeat on the normal cooldown, others on four times that."""
        for hw_error in hardware_errors:
            cooldown = self.cfg.alert_cooldown
            if hw_error.get('severity', 'warning') != 'critical':
                cooldown *= 4
            if now - self.last_hardware_error_alert_times.get(_hardware_error_key(hw_error), 0) >= cooldown:
                return True
        return False

    def _send_crash_embed(self, title: str, description: str, fields: list[Field], footer: str, label: str) -> None:
        ctx = self.ctx
        if ctx.deliver(
            ctx.crash_webhook,
            'send_embed',
            "crash_webhook send_embed",
            title=title,
            description=description,
            fields=fields,
            footer=footer,
            timestamp=True,
        ):
            self.log.info(f"    Discord kernel {label} alert sent")
        else:
            self.log.warning(f"    Discord kernel {label} alert failed")

    def _alert_reboot(self, reboot_info: dict[str, Any]) -> None:
        previous, current = reboot_info.get('previous_boot_time'), reboot_info.get('current_boot_time')
        self.log.critical(
            f"[KERNEL] Unexpected system reboot detected! Previous boot: {previous}, Current boot: {current}"
        )
        if not self.ctx.crash_webhook:
            return
        self._send_crash_embed(
            'Kernel Alert: Unexpected System Reboot',
            'System reboot detected - possible kernel crash or unexpected shutdown',
            [
                _field('Event Type', 'Unexpected System Reboot', inline=False),
                _field('Previous Boot Time', _format_boot_time(previous)),
                _field('Current Boot Time', _format_boot_time(current)),
                _field('Status', 'System may have crashed or was unexpectedly rebooted', inline=False),
            ],
            'Server Monitor - Kernel',
            'reboot',
        )

    def _alert_early_warnings(
        self, critical_warnings: list[dict[str, Any]], hardware_errors: list[dict[str, Any]], now: float
    ) -> None:
        self.log.warning(
            f"[KERNEL] Early warning signs detected: {len(critical_warnings)} critical warnings, "
            f"{len(hardware_errors)} hardware errors"
        )
        if not self.ctx.crash_webhook:
            return

        fields = [
            _field('Warning Type', 'Early Warning - System may be heading towards crash', inline=False),
            _field('Critical Warnings', str(len(critical_warnings))),
            _field('Hardware Errors', str(len(hardware_errors))),
        ]
        if hardware_errors:
            details = [describe_hardware_error(e) for e in hardware_errors[:MAX_HARDWARE_ERRORS_DISPLAY]]
            fields[2] = _field(
                f'Hardware Error Details ({len(hardware_errors)} total)', '\n'.join(details), inline=False
            )
        if critical_warnings:
            details = [
                f"**{w.get('keyword', 'unknown')}**: {w.get('message', '')[:150]}" for w in critical_warnings[:5]
            ]
            fields.append(_field('Critical Warnings', '\n'.join(details), inline=False))
        fields.append(
            _field(
                'Action Required',
                'System showing signs of instability. Monitor closely and investigate hardware/system issues.',
                inline=False,
            )
        )

        ctx = self.ctx
        success = ctx.deliver(
            ctx.crash_webhook,
            'send_embed',
            "crash_webhook send_embed",
            title='Kernel Alert: Early Warning Signs Detected',
            description='System showing early warning signs that may indicate impending kernel crash',
            fields=fields,
            footer='Server Monitor - Kernel Warning',
            timestamp=True,
        )
        for hw_error in hardware_errors:
            self.last_hardware_error_alert_times[_hardware_error_key(hw_error)] = now
        if success:
            self.log.info("    Discord kernel early warning alert sent")
        else:
            self.log.warning("    Discord kernel early warning alert failed")

    def _alert_crashes(self, crashes: list[dict[str, Any]]) -> None:
        self.log.critical(f"[KERNEL] {len(crashes)} kernel crash/panic detected!")
        if not self.ctx.crash_webhook:
            return

        by_type: dict[str, list[dict[str, Any]]] = {}
        for crash in crashes:
            by_type.setdefault(crash.get('keyword', 'unknown'), []).append(crash)

        fields = [
            _field('Total Crashes', str(len(crashes))),
            _field('Check Period', f'{self.cfg.kernel_check_since_minutes} minutes'),
        ]
        details = [
            f"**{crash_type}** ({len(items)} occurrence(s)) - Source: {items[0].get('source', 'unknown')}\n"
            f"`{items[0].get('message', '')[:200]}`"
            for crash_type, items in list(by_type.items())[:MAX_CRASH_TYPES_DISPLAY]
        ]
        if details:
            fields.append(_field('Crash Details', '\n\n'.join(details), inline=False))
        if len(by_type) > 5:
            fields.append(
                _field(
                    'Note', f'Showing first 5 of {len(by_type)} crash types. Check logs for full details.', inline=False
                )
            )

        self._send_crash_embed(
            'Kernel Alert: Crash/Panic Detected',
            'Kernel crashes, panics, or oops detected in system logs',
            fields,
            'Server Monitor - Kernel',
            'crash',
        )
