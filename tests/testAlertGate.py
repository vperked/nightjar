from core.alerts import AlertGate, AlertGates


def make(base=300.0, max_cd=3600.0, backoff=2.0, recovery=300.0):
    return AlertGate(base, max_cd, backoff, recovery)


def run(gate, start, end, step=5.0):
    """Report the condition every step seconds and return the times alerts fired."""
    sent = []
    t = start
    while t <= end:
        if gate.should_alert(t):
            gate.mark_sent(t)
            sent.append(t)
        t += step
    return sent


def test_first_alert_is_immediate():
    assert make().should_alert(1000.0) is True


def test_repeats_back_off_and_cap():
    sent = run(make(), 0, 3 * 3600)

    gaps = [b - a for a, b in zip(sent, sent[1:])]
    assert gaps[:5] == [300, 600, 1200, 2400, 3600]
    assert max(gaps) == 3600


def test_short_dip_does_not_reset_episode():
    gate = make()
    assert gate.should_alert(0.0)
    gate.mark_sent(0.0)

    # condition clears for 60s then returns: still the same episode, still cooling down
    assert gate.should_alert(120.0) is False
    assert gate.should_alert(200.0) is False


def test_recovery_after_quiet_period_starts_new_episode():
    gate = make()
    run(gate, 0, 2000)
    assert gate.sent_count > 1

    assert gate.should_alert(2000 + 301.0 + 5) is True
    assert gate.sent_count == 0


def test_flapping_metric_alerts_at_base_cooldown_not_every_cycle():
    gate = make()
    sent = []
    t = 0.0
    while t <= 1200:
        # above threshold for 20s, below for 20s
        if int(t // 20) % 2 == 0 and gate.should_alert(t):
            gate.mark_sent(t)
            sent.append(t)
        t += 5
    assert len(sent) <= 3


def test_zero_cooldown_disables_throttling():
    gate = make(base=0.0)
    gate.mark_sent(0.0)

    assert gate.should_alert(1.0) is True


def test_backoff_of_one_keeps_fixed_cooldown():
    sent = run(make(backoff=1.0), 0, 1500)

    assert sent == [0, 300, 600, 900, 1200, 1500]


def test_gates_are_independent_and_pruned():
    gates = AlertGates(300.0, 3600.0, 2.0, 300.0)
    gates.get('a', 0.0).should_alert(0.0)
    gates.get('a', 0.0).mark_sent(0.0)

    assert gates.get('b', 0.0).should_alert(1.0) is True
    for i in range(AlertGates.PRUNE_ABOVE + 1):
        gates.get(f'k{i}', 10_000.0)
    assert len(gates._gates) <= AlertGates.PRUNE_ABOVE + 1
