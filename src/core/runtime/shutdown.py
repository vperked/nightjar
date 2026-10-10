"""
Shutdown: signal handling, graceful stop, crash reporting and final bookkeeping.
"""

import atexit
import contextlib
import platform
import signal
import threading
import time
import traceback

from core.cache import clear_cache_on_exit
from core.observability import Component, get_logger
from core.runtime.context import MonitorContext
from core.web import stop_server

logger = get_logger(Component.CORE)


class ShutdownState:
    """Why and how the process is stopping; shared with the signal and atexit handlers."""

    def __init__(self):
        self.graceful_shutdown = False
        self.reason = 'unknown'
        self.time = None
        self.in_progress = False
        self.lock = threading.Lock()

    def on_signal(self, signum, frame):
        """Mark a graceful shutdown and unwind the loop via KeyboardInterrupt."""
        signal_name = signal.Signals(signum).name if hasattr(signal, 'Signals') else str(signum)
        with self.lock:
            if self.in_progress:
                return
            self.graceful_shutdown = True
            self.reason = f'signal_{signal_name}'
            self.time = time.time()
            self.in_progress = True
        logger.info(f"Received {signal_name} signal - initiating graceful shutdown...")
        raise KeyboardInterrupt

    def on_exit(self):
        """At interpreter exit, record a crash if nothing marked the shutdown as graceful."""
        with self.lock:
            if self.graceful_shutdown:
                return
            if self.reason == 'unknown':
                self.reason = 'atexit_unknown_crash'
                logger.warning("Program exited via atexit without graceful shutdown flag - possible crash")
            elif self.reason != 'crash_exception':
                self.graceful_shutdown = True

    def install(self) -> None:
        """Register signal and atexit handlers."""
        signals = [signal.SIGTERM, signal.SIGINT]
        if platform.system() != 'Windows' and hasattr(signal, 'SIGQUIT'):
            signals.append(signal.SIGQUIT)
        for sig in signals:
            try:
                signal.signal(sig, self.on_signal)
            except (AttributeError, ValueError):
                pass
        atexit.register(self.on_exit)


def handle_interrupt(ctx: MonitorContext, state: ShutdownState, started_at: float, web_server_started: bool) -> None:
    """Stop helpers and the web UI, then send the shutdown notice for graceful stops."""
    cfg = ctx.cfg
    shutdown_start = time.time()

    with state.lock:
        is_graceful = state.graceful_shutdown
        shutdown_reason = state.reason
        state.in_progress = True
        state.time = shutdown_start

    if is_graceful:
        logger.info(f"Monitoring stopped gracefully ({shutdown_reason})")
    else:
        logger.info("Monitoring stopped by user (Ctrl+C)")

    if ctx.watchdog_monitor:
        try:
            ctx.watchdog_monitor.stop()
        except KeyboardInterrupt:
            if not is_graceful:
                logger.warning("Watchdog stop interrupted, exiting immediately")
                raise

    uptime_seconds = shutdown_start - started_at

    logger.info("Shutting down...")

    if cfg.freeze_monitor_enabled and ctx.freeze_monitor:
        try:
            ctx.freeze_monitor.stop_watchdog()
        except (KeyboardInterrupt, Exception) as e:
            logger.debug(f"Freeze monitor stop failed: {e}")

    if web_server_started:
        try:
            stop_server(timeout=3.0)
        except KeyboardInterrupt:
            pass
        except Exception as e:
            logger.warning(f"Error stopping web server: {e}")

    shutdown_seconds = time.time() - shutdown_start
    logger.info(f"Shutdown complete ({shutdown_seconds:.2f}s)")

    if ctx.webhook and is_graceful:
        try:
            logger.info("Sending shutdown notification...")
            if ctx.deliver(
                ctx.webhook,
                'send_shutdown_notification',
                "webhook send_shutdown_notification",
                uptime_seconds=uptime_seconds,
                shutdown_seconds=shutdown_seconds,
            ):
                logger.info("Shutdown notification sent")
            else:
                logger.warning("Shutdown notification failed")
        except (KeyboardInterrupt, Exception) as e:
            logger.warning(f"Shutdown notification skipped: {e}")
    elif not is_graceful:
        logger.debug("Skipping shutdown notification (not a graceful shutdown)")


def handle_crash(
    ctx: MonitorContext, state: ShutdownState, e: Exception, started_at: float, web_server_started: bool
) -> None:
    """Report an exception that escaped the monitoring loop and tear down helpers."""
    cfg = ctx.cfg
    with state.lock:
        state.graceful_shutdown = False
        state.reason = 'crash_exception'
        state.time = time.time()

    logger.error(f"Unexpected error in monitoring loop: {e}")
    logger.error(f"Traceback: {traceback.format_exc()}")
    logger.critical("CRITICAL: Monitoring loop crashed (unhandled exception).")

    crash_webhook_to_use = ctx.crash_webhook if ctx.crash_webhook else ctx.webhook
    if crash_webhook_to_use:
        try:
            traceback_text = traceback.format_exc()
            if len(traceback_text) > 1500:
                traceback_text = traceback_text[:1500] + "... (truncated)"

            ctx.deliver(
                crash_webhook_to_use,
                'send_embed',
                "crash_webhook_to_use send_embed",
                title='CRITICAL: Server Monitor Crashed',
                description='The monitoring loop crashed due to an unhandled exception',
                fields=[
                    {'name': 'Error Type', 'value': type(e).__name__, 'inline': True},
                    {'name': 'Error Message', 'value': str(e)[:500], 'inline': False},
                    {
                        'name': 'Uptime Before Crash',
                        'value': f"{(time.time() - started_at):.1f} seconds",
                        'inline': True,
                    },
                    {'name': 'Traceback', 'value': f"```\n{traceback_text}\n```", 'inline': False},
                    {'name': 'Status', 'value': 'CRITICAL - Program crashed unexpectedly', 'inline': False},
                ],
                color=0x000000,
                footer='Server Monitor - Crash Alert',
                timestamp=True,
            )
            logger.info("Crash alert sent to Discord")
        except Exception as webhook_error:
            logger.warning(f"Failed to send crash alert: {webhook_error}")

    ctx.report_error(e, context="CRITICAL: Monitoring loop crash", include_traceback=True)

    if ctx.watchdog_monitor:
        ctx.watchdog_monitor.stop()

    logger.info("Shutting down due to critical error...")
    if cfg.freeze_monitor_enabled and ctx.freeze_monitor:
        with contextlib.suppress(Exception):
            ctx.freeze_monitor.stop_watchdog()
    if web_server_started:
        with contextlib.suppress(Exception):
            stop_server()


def finalize(state: ShutdownState) -> None:
    """Clear caches and log how the run ended."""
    with state.lock:
        final_reason = state.reason
        is_graceful_final = state.graceful_shutdown

    clear_cache_on_exit(verbose=True)

    if is_graceful_final:
        logger.info(f"Server Monitor stopped gracefully ({final_reason})")
    else:
        logger.warning(f"Server Monitor stopped unexpectedly ({final_reason}) - may have crashed")
