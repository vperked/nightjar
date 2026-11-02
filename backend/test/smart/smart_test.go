package smart_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"servermonitor/internal/smart"
)

const hdd = `{"smartctl":{"exit_status":0},"model_name":"WDC WD80EFAX","serial_number":"S1","user_capacity":{"bytes":8001563222016},"rotation_rate":5400,
"smart_status":{"passed":true},"temperature":{"current":41},"power_on_time":{"hours":30000},
"ata_smart_attributes":{"table":[{"id":5,"name":"Reallocated_Sector_Ct","raw":{"value":8}},{"id":197,"name":"Current_Pending_Sector","raw":{"value":0}},{"id":199,"name":"UDMA_CRC_Error_Count","raw":{"value":0}}]}}`

const nvme = `{"smartctl":{"exit_status":0},"model_name":"WDC PC SN530","serial_number":"N1","user_capacity":{"bytes":512110190592},
"smart_status":{"passed":true},"temperature":{"current":54},"power_on_time":{"hours":19633},
"nvme_smart_health_information_log":{"critical_warning":0,"available_spare":100,"available_spare_threshold":10,"percentage_used":214,"media_errors":0,"unsafe_shutdowns":330}}`

const asleep = `{"smartctl":{"exit_status":2,"messages":[{"string":"Device is in STANDBY mode, exit(2)","severity":"information"}]}}`

type fake struct{ sda, nvme string }

func (f *fake) run(_ context.Context, args ...string) ([]byte, error) {
	switch args[len(args)-1] {
	case "--json":
		return []byte(`{"devices":[{"name":"/dev/sda","type":"sat"},{"name":"/dev/nvme0","type":"nvme"}]}`), nil
	case "/dev/sda":
		return []byte(f.sda), nil
	case "/dev/nvme0":
		return []byte(f.nvme), nil
	}
	return nil, errors.New("unexpected " + strings.Join(args, " "))
}

var limits = smart.Limits{HDD: 55, SSD: 70}

func TestReadsAndAssessesDrives(t *testing.T) {
	f := &fake{sda: hdd, nvme: nvme}
	r := smart.NewReader()
	r.Run = f.run
	drives, err := r.Read(context.Background(), time.Now(), limits)
	if err != nil || len(drives) != 2 {
		t.Fatalf("%v %v", drives, err)
	}
	sda, nv := drives[0], drives[1]
	if !sda.Rotational || *sda.Reallocated != 8 || sda.Health != "warning" || sda.Problems[0] != "8 reallocated sectors" {
		t.Fatalf("hdd: %+v", sda)
	}
	if nv.Rotational || *nv.PercentUsed != 214 || nv.Health != "warning" || !strings.Contains(nv.Problems[0], "214% of its rated write endurance") {
		t.Fatalf("nvme: %+v", nv)
	}
}

func TestSleepingDiskKeepsItsLastReading(t *testing.T) {
	f := &fake{sda: hdd, nvme: nvme}
	r := smart.NewReader()
	r.Run = f.run
	r.Read(context.Background(), time.Unix(1000, 0), limits)
	f.sda = asleep
	drives, _ := r.Read(context.Background(), time.Unix(2000, 0), limits)
	if sda := drives[0]; !sda.Asleep || sda.Model != "WDC WD80EFAX" || sda.Checked != 1000 {
		t.Fatalf("asleep: %+v", sda)
	}

	// After a restart, the saved reading stands in until the disk wakes.
	fresh := smart.NewReader()
	fresh.Run = f.run
	fresh.Restore(drives)
	got, _ := fresh.Read(context.Background(), time.Unix(3000, 0), limits)
	if got[0].Model != "WDC WD80EFAX" || !got[0].Asleep {
		t.Fatalf("restored: %+v", got[0])
	}
}

func TestAssessVerdicts(t *testing.T) {
	yes, no := true, false
	i := func(v int) *int { return &v }
	cases := []struct {
		name string
		d    smart.Drive
		want string
	}{
		{"healthy", smart.Drive{Passed: &yes, Temperature: i(40)}, "good"},
		{"failed self-assessment", smart.Drive{Passed: &no}, "failing"},
		{"failing attribute", smart.Drive{Passed: &yes, FailingAttributes: []string{"Reallocated_Sector_Ct"}}, "failing"},
		{"read-only nvme", smart.Drive{Passed: &yes, CriticalWarning: i(0x08)}, "failing"},
		{"hot nvme is a warning, not a failure", smart.Drive{Passed: &yes, CriticalWarning: i(0x02)}, "warning"},
		{"hot hard disk", smart.Drive{Passed: &yes, Rotational: true, Temperature: i(58)}, "warning"},
		{"warm ssd is fine", smart.Drive{Passed: &yes, Temperature: i(58)}, "good"},
		{"low spare", smart.Drive{Passed: &yes, AvailableSpare: i(15), SpareThreshold: i(10)}, "warning"},
		{"no data", smart.Drive{}, "unknown"},
	}
	for _, c := range cases {
		smart.Assess(&c.d, limits)
		if c.d.Health != c.want {
			t.Errorf("%s: %s %v", c.name, c.d.Health, c.d.Problems)
		}
	}
}

func TestMissingSmartctl(t *testing.T) {
	r := smart.NewReader()
	r.Run = func(context.Context, ...string) ([]byte, error) { return nil, exec.ErrNotFound }
	if _, err := r.Read(context.Background(), time.Now(), limits); !errors.Is(err, smart.ErrNoSmartctl) {
		t.Fatalf("%v", err)
	}
	if _, problem := r.Drives(); !strings.Contains(problem, "apt install smartmontools") {
		t.Fatalf("the reason is kept for the dashboard: %q", problem)
	}
}
