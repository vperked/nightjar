"""
Alert throttling: escalating repeat intervals and flap suppression.
"""



class AlertGate:
    """
    Decides whether an ongoing condition may alert again.

    The first alert in an episode is sent immediately. While the condition keeps
    being reported, repeats wait base_cooldown, then base_cooldown * backoff, and so
    on up to max_cooldown. An episode only ends once the condition has not been
    reported for recovery_seconds, so a metric hovering around its threshold stays
    one episode instead of re-alerting every time it dips and rises.
    """

    def __init__(self, base_cooldown: float, max_cooldown: float,
                 backoff: float, recovery_seconds: float):
        self.base_cooldown = max(0.0, base_cooldown)
        self.max_cooldown = max(self.base_cooldown, max_cooldown)
        self.backoff = max(1.0, backoff)
        self.recovery_seconds = max(0.0, recovery_seconds)
        self.last_seen = None
        self.last_sent = None
        self.sent_count = 0

    def current_cooldown(self) -> float:
        """Seconds that must pass before the next repeat in this episode."""
        if self.sent_count <= 1:
            return self.base_cooldown
        return min(self.max_cooldown, self.base_cooldown * self.backoff ** (self.sent_count - 1))

    def should_alert(self, now: float) -> bool:
        """
        Report that the condition is currently true and ask whether to alert.

        Args:
            now: Current time in seconds

        Returns:
            True when an alert should be sent now
        """
        if self.last_seen is not None and now - self.last_seen > self.recovery_seconds:
            self.last_sent = None
            self.sent_count = 0
        self.last_seen = now

        if self.base_cooldown <= 0 or self.last_sent is None:
            return True
        return now - self.last_sent >= self.current_cooldown()

    def mark_sent(self, now: float) -> None:
        """Record that an alert went out (or was logged) at this time."""
        self.last_sent = now
        self.sent_count += 1

    def seconds_until_next(self, now: float) -> float:
        """Remaining wait before the next repeat, for debug logging."""
        if self.last_sent is None:
            return 0.0
        return max(0.0, self.current_cooldown() - (now - self.last_sent))


class AlertGates:
    """Lazily created AlertGate per alert key, with idle gates pruned."""

    PRUNE_ABOVE = 256

    def __init__(self, base_cooldown: float, max_cooldown: float,
                 backoff: float, recovery_seconds: float):
        self._args = (base_cooldown, max_cooldown, backoff, recovery_seconds)
        self._gates: dict[str, AlertGate] = {}

    def get(self, key: str, now: float = 0.0) -> AlertGate:
        """Return the gate for key, creating it on first use."""
        gate = self._gates.get(key)
        if gate is None:
            if len(self._gates) >= self.PRUNE_ABOVE:
                self._prune(now)
            gate = self._gates[key] = AlertGate(*self._args)
        return gate

    def _prune(self, now: float) -> None:
        """Drop gates whose episode is over (e.g. exited processes)."""
        recovery = self._args[3]
        stale = [
            key for key, gate in self._gates.items()
            if gate.last_seen is None or now - gate.last_seen > recovery
        ]
        for key in stale:
            del self._gates[key]


__all__ = ['AlertGate', 'AlertGates']
