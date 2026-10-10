"""
qBittorrent torrent tracking: stall recovery, cleanup policy and progress notifications.
"""

from typing import Any

from core.config import MAX_TRACKED_TORRENTS
from core.observability import Component, get_logger
from core.qbittorrent import (
    DOWNLOADING_STATES,
    SEEDING_ACTIVE_STATES,
    is_download_stalled,
    should_remove_torrent,
    stalled_action_plan,
)
from core.runtime.handlers.base import Handler
from core.runtime.snapshot import Snapshot
from core.timeout import run_with_timeout


QBITTORRENT_CHECK_INTERVAL = 2
_NO_ACTIONS = {'success': False, 'actions': []}


def _new_tracking(torrent: dict[str, Any], state: str, now: float, stalled: bool) -> dict[str, Any]:
    return {
        'state': state,
        'progress': torrent.get('progress', 0.0),
        'name': torrent.get('name', 'Unknown'),
        'notified_download': False,
        'notified_seeding': False,
        'stalled_since': now if stalled else None,
        'unstalled_since': None,
        'last_stalled_action': None,
        'stalled_recovery_attempts': 0,
        'notified_stalled': False,
        'last_ratio_action': None,
        'seen_at': now,
    }


class TorrentWatcher(Handler):
    """Tracks every torrent across cycles to recover stalls, apply the cleanup policy and notify."""

    log = get_logger(Component.QBITTORRENT)

    def __init__(self, ctx):
        super().__init__(ctx)
        self.qbittorrent_check_counter = 0
        self.tracked_torrents: dict[str, dict[str, Any]] = {}

    def check(self, snap: Snapshot) -> None:
        """Every few cycles, fetch all torrents and act on each one."""
        ctx = self.ctx
        if not ctx.qbittorrent_monitor:
            return
        self.qbittorrent_check_counter += 1
        if self.qbittorrent_check_counter >= QBITTORRENT_CHECK_INTERVAL:
            self.qbittorrent_check_counter = 0
            if ctx.qbittorrent_monitor.is_available():
                try:
                    self._scan(snap.current_time)
                except (ConnectionError, TimeoutError, ValueError) as e:
                    self.log.debug(f"qBittorrent check failed: {e}")
                except Exception as e:
                    self.log.warning(f"Unexpected error checking qBittorrent: {e}")
        ctx.heartbeat()

    def _scan(self, now: float) -> None:
        monitor = self.ctx.qbittorrent_monitor
        torrents = run_with_timeout(
            monitor.get_all_torrents, timeout=15.0, default_value=[], operation_name="qBittorrent get all torrents"
        )
        if not torrents and not monitor.last_fetch_ok:
            # An empty list from a failed fetch must not be mistaken for "every torrent is gone".
            raise ConnectionError("qBittorrent torrent list unavailable")

        current_hashes = set()
        for torrent in torrents:
            torrent_hash = torrent.get('hash')
            if torrent_hash:
                current_hashes.add(torrent_hash)
                self._process(torrent_hash, torrent, now)
        self._prune(current_hashes)

    def _process(self, torrent_hash: str, torrent: dict[str, Any], now: float) -> None:
        state = str(torrent.get('state', '')).lower()
        stalled = is_download_stalled(torrent)

        tracked = self.tracked_torrents.get(torrent_hash)
        if tracked is None:
            tracked = self.tracked_torrents[torrent_hash] = _new_tracking(torrent, state, now, stalled)
        prev_state = tracked['state']

        if stalled:
            self._handle_stall(torrent_hash, torrent, tracked, now)
        else:
            self._handle_unstalled(tracked, now)

        remove_reason = should_remove_torrent(torrent, min_ratio=self.cfg.seeding_ratio_limit)
        if remove_reason == 'inactive' and not self.cfg.inactive_torrent_auto_remove:
            remove_reason = None
        if remove_reason:
            self._apply_cleanup_policy(torrent_hash, torrent, tracked, state, remove_reason, now)

        self._notify_progress(torrent, tracked, state, prev_state)
        tracked['state'] = state
        tracked['progress'] = torrent.get('progress', 0.0)

    def _handle_stall(self, torrent_hash: str, torrent: dict[str, Any], tracked: dict[str, Any], now: float) -> None:
        cfg, ctx = self.cfg, self.ctx
        tracked['unstalled_since'] = None
        if tracked.get('stalled_since') is None:
            tracked['stalled_since'] = now
        duration = now - tracked['stalled_since']
        plan = stalled_action_plan(
            tracked, now, cfg.stalled_threshold, cfg.stalled_action_cooldown, cfg.stalled_max_recovery_attempts
        )
        if not (plan['recover'] or plan['alert'] or plan['abandon']):
            return

        name = torrent.get('name', 'Unknown')
        actions: list[str] = []
        recovery_ok = True
        if plan['recover'] and cfg.stalled_auto_handle:
            recovery = (
                run_with_timeout(
                    lambda: ctx.qbittorrent_monitor.recover_stalled_torrent(torrent_hash),
                    timeout=15.0,
                    default_value=_NO_ACTIONS,
                    operation_name="Recover stalled torrent",
                )
                or _NO_ACTIONS
            )
            actions = list(recovery.get('actions') or [])
            recovery_ok = bool(actions)
            tracked['stalled_recovery_attempts'] = int(tracked.get('stalled_recovery_attempts') or 0) + 1
            if recovery_ok:
                self.log.warning(
                    f"[qBittorrent] Recovering stalled torrent: {name} "
                    f"(stalled for {duration:.0f}s; actions: {', '.join(actions)})"
                )
            else:
                self.log.warning(f"[qBittorrent] Failed to recover stalled torrent: {name} (stalled for {duration:.0f}s)")

            attempts = int(tracked.get('stalled_recovery_attempts') or 0)
            if attempts >= cfg.stalled_max_recovery_attempts:
                self.log.info(
                    f"[qBittorrent] Stopping stalled recovery for {name} "
                    f"after {attempts} attempt(s); torrent is still stalled"
                )
        elif plan['recover']:
            tracked['stalled_recovery_attempts'] = cfg.stalled_max_recovery_attempts
            self.log.warning(f"[qBittorrent] Torrent stalled: {name} (stalled for {duration:.0f}s)")

        if plan['alert']:
            tracked['notified_stalled'] = self._alert_stalled(torrent, duration, actions)

        if recovery_ok:
            tracked['last_stalled_action'] = now
        else:
            tracked['last_stalled_action'] = now - cfg.stalled_action_cooldown + cfg.stalled_retry_backoff

    def _alert_stalled(self, torrent: dict[str, Any], duration: float, actions: list[str]) -> bool:
        """Send the one-per-episode stall alert. Returns whether the episode now counts as notified."""
        ctx = self.ctx
        if not ctx.qbittorrent_webhook:
            return True
        success = ctx.deliver(
            ctx.qbittorrent_webhook,
            'send_stalled_torrent_alert',
            "Stalled torrent alert webhook",
            torrent,
            duration,
            actions_taken=actions or None,
        )
        ctx.metrics_collector.record_alert('torrent_stalled')
        ctx.metrics_collector.record_webhook_result(success)
        if success:
            self.log.info("    Discord stalled torrent alert sent")
        else:
            self.log.warning("    Discord stalled torrent alert failed")
        return success

    def _handle_unstalled(self, tracked: dict[str, Any], now: float) -> None:
        """End a stall episode once the torrent has stayed unstalled for the grace period."""
        if tracked.get('stalled_since') is None:
            return
        unstalled_since = tracked.get('unstalled_since')
        if unstalled_since is None:
            tracked['unstalled_since'] = now
        elif (now - unstalled_since) >= self.cfg.stalled_recovery_grace:
            tracked.update(
                stalled_since=None,
                unstalled_since=None,
                stalled_recovery_attempts=0,
                notified_stalled=False,
                last_stalled_action=None,
            )

    def _apply_cleanup_policy(
        self,
        torrent_hash: str,
        torrent: dict[str, Any],
        tracked: dict[str, Any],
        state: str,
        remove_reason: str,
        now: float,
    ) -> None:
        cfg, ctx = self.cfg, self.ctx
        last_action = tracked.get('last_ratio_action') or 0.0
        if (now - last_action) < cfg.seeding_ratio_action_cooldown:
            return

        name = torrent.get('name', 'Unknown')
        ratio = torrent.get('ratio', 0.0) or 0.0
        # Paused downloads may be partial; never delete their files.
        delete_files = cfg.seeding_ratio_delete_files and remove_reason == 'ratio'
        result = (
            run_with_timeout(
                lambda: ctx.qbittorrent_monitor.handle_ratio_complete_torrent(torrent_hash, delete_files=delete_files),
                timeout=15.0,
                default_value=_NO_ACTIONS,
                operation_name="Handle torrent cleanup",
            )
            or _NO_ACTIONS
        )
        actions = list(result.get('actions') or [])

        if remove_reason == 'inactive':
            if actions:
                self.log.info(
                    f"[qBittorrent] Removing inactive torrent: {name} (state={state}; actions: {', '.join(actions)})"
                )
            else:
                self.log.warning(f"[qBittorrent] Failed to remove inactive torrent: {name} (state={state})")
        else:
            limit = f"ratio {ratio:.2f} >= {cfg.seeding_ratio_limit:.2f}"
            if actions:
                self.log.info(f"[qBittorrent] Ratio limit reached: {name} ({limit}; actions: {', '.join(actions)})")
            else:
                self.log.warning(f"[qBittorrent] Failed to remove ratio-complete torrent: {name} ({limit})")

        if ctx.qbittorrent_webhook:
            if remove_reason == 'inactive':
                success = ctx.deliver(
                    ctx.qbittorrent_webhook,
                    'send_inactive_torrent_alert',
                    "Inactive torrent alert webhook",
                    torrent,
                    actions_taken=actions or None,
                )
                ctx.metrics_collector.record_alert('torrent_inactive')
            else:
                success = ctx.deliver(
                    ctx.qbittorrent_webhook,
                    'send_ratio_limit_alert',
                    "Ratio limit alert webhook",
                    torrent,
                    cfg.seeding_ratio_limit,
                    actions_taken=actions or None,
                )
                ctx.metrics_collector.record_alert('torrent_ratio_limit')
            ctx.metrics_collector.record_webhook_result(success)
            if success:
                self.log.info("    Discord torrent cleanup alert sent")
            else:
                self.log.warning("    Discord torrent cleanup alert failed")

        if actions:
            tracked['last_ratio_action'] = now
        else:
            tracked['last_ratio_action'] = now - cfg.seeding_ratio_action_cooldown + cfg.seeding_ratio_retry_backoff

    def _notify_progress(self, torrent: dict[str, Any], tracked: dict[str, Any], state: str, prev_state: str) -> None:
        """Announce a finished download and the start of seeding, once each."""
        is_seeding = state in SEEDING_ACTIVE_STATES
        progress = torrent.get('progress', 0.0)
        name = torrent.get('name', 'Unknown')

        if is_seeding and progress >= 1.0 and not tracked['notified_download'] and prev_state in DOWNLOADING_STATES:
            self._notify(
                f"[qBittorrent] Download complete: {name}",
                'send_download_complete_alert',
                "Download complete alert webhook",
                "download complete notification",
                torrent,
            )
            tracked['notified_download'] = True

        if is_seeding and prev_state not in SEEDING_ACTIVE_STATES and not tracked['notified_seeding']:
            self._notify(
                f"[qBittorrent] Torrent seeding: {name}",
                'send_seeding_notification',
                "Seeding notification webhook",
                "seeding notification",
                torrent,
            )
            tracked['notified_seeding'] = True

    def _notify(self, message: str, method: str, operation: str, label: str, torrent: dict[str, Any]) -> None:
        ctx = self.ctx
        if not ctx.qbittorrent_webhook:
            return
        self.log.info(message)
        if ctx.deliver(ctx.qbittorrent_webhook, method, operation, torrent):
            self.log.info(f"    Discord {label} sent")
        else:
            self.log.warning(f"    Discord {label} failed")

    def _prune(self, current_hashes: set) -> None:
        """Forget removed torrents and cap the tracking table."""
        for torrent_hash in [h for h in self.tracked_torrents if h not in current_hashes]:
            del self.tracked_torrents[torrent_hash]

        overflow = len(self.tracked_torrents) - MAX_TRACKED_TORRENTS
        if overflow > 0:
            self.log.warning(f"Tracked torrents dict too large ({len(self.tracked_torrents)}), clearing old entries")
            oldest = sorted(self.tracked_torrents, key=lambda h: self.tracked_torrents[h].get('seen_at') or 0.0)[
                :overflow
            ]
            for old_hash in oldest:
                del self.tracked_torrents[old_hash]
