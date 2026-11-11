package kernel_test

import (
	"fmt"
	"strings"
	"testing"

	"nightjar/internal/kernel"
)

// warning is one repeat of a driver stuck in a loop, the way dmesg prints it: the message, then the
// register and stack dump that comes with every repeat.
func warning(t float64, addr string) string {
	lines := []string{
		"nouveau %s: timeout",
		"------------[ cut here ]------------",
		"WARNING: CPU: 3 PID: 1234 at drivers/gpu/drm/nouveau/nvkm/subdev/timer/base.c:123 nvkm_timer_wait_test+0x5a/0x60 [nouveau]",
		"Modules linked in: snd_hda_codec_hdmi nouveau mxm_wmi drm_ttm_helper ttm",
		" btusb gpu_sched typec_ucsi btrtl snd_hda_core",
		"RIP: 0010:nvkm_timer_wait_test+0x5a/0x60 [nouveau]",
		"RAX: 0000000000000000 RBX: ffff8f2a01234567 RCX: 0000000000000000",
		"R10: 0000000000000000 R11: 0000000000000000 R12: ffff8f2a01234567",
		"Call Trace:",
		" <TASK>",
		" ? __pfx_kthread+0x10/0x10",
		" </TASK>",
		"---[ end trace 0000000000000000 ]---",
	}
	var b strings.Builder
	for i, l := range lines {
		if strings.Contains(l, "%s") {
			l = fmt.Sprintf(l, addr)
		}
		fmt.Fprintf(&b, "[%12.6f] %s\n", t+float64(i)*0.00001, l)
	}
	return b.String()
}

func TestAKernelRepeatingAWarningIsSeenAndNamed(t *testing.T) {
	var b strings.Builder
	for i := range 200 { // 2600 lines in a tenth of a second
		b.WriteString(warning(2062668+float64(i)*0.0005, "0000:01:00.0"))
	}
	f := kernel.ParseFlood([]byte(b.String()), 2062668.2)
	if f == nil || f.PerSecond < 10000 {
		t.Fatalf("flood: %+v", f)
	}
	if f.Top != "nouveau 0000:01:00.0: timeout" {
		t.Fatalf("it names the message, not the dump that comes with it: %q", f.Top)
	}

	// A quiet kernel: the same number of lines, over a day.
	var q strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&q, "[%12.6f] usb 1-%d: new high-speed USB device number %d\n", float64(i)*43.2, i%4, i)
	}
	if f := kernel.ParseFlood([]byte(q.String()), 2000*43.2); f != nil {
		t.Fatalf("a quiet kernel is not a flood: %+v", f)
	}

	// The same flood, over two minutes ago: the kernel went quiet since, so it is not flooding now
	// (and the journal's crash checks, skipped during a flood, run again).
	if f := kernel.ParseFlood([]byte(b.String()), 2062668+130); f != nil {
		t.Fatalf("an old flood is still a flood: %+v", f)
	}
	// Without the time, measured to the newest message, as before.
	if f := kernel.ParseFlood([]byte(b.String()), 0); f == nil {
		t.Fatal("a flood without the time")
	}
}
