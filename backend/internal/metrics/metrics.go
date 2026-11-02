// Package metrics counts alerts, errors, webhook outcomes and monitor timings for the periodic report.
package metrics

import (
	"sync"
	"time"

	"servermonitor/internal/format"
)

const maxHistory = 1000

// Collector is safe for concurrent use.
type Collector struct {
	mu              sync.Mutex
	start           time.Time
	alertCounts     map[string]int
	alertStamps     map[string][]time.Time
	errorCounts     map[string]int
	webhookSuccess  int
	webhookFailure  int
	executionTimes  map[string][]time.Duration
	executionStarts map[string]time.Time
}

func New() *Collector {
	return &Collector{
		start:           time.Now(),
		alertCounts:     map[string]int{},
		alertStamps:     map[string][]time.Time{},
		errorCounts:     map[string]int{},
		executionTimes:  map[string][]time.Duration{},
		executionStarts: map[string]time.Time{},
	}
}

func (c *Collector) RecordAlert(kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.alertCounts[kind]++
	stamps := append(c.alertStamps[kind], now)
	cutoff := now.Add(-time.Hour)
	for len(stamps) > 0 && !stamps[0].After(cutoff) {
		stamps = stamps[1:]
	}
	c.alertStamps[kind] = stamps
}

func (c *Collector) RecordError(kind string) {
	c.mu.Lock()
	c.errorCounts[kind]++
	c.mu.Unlock()
}

func (c *Collector) RecordWebhookResult(ok bool) {
	c.mu.Lock()
	if ok {
		c.webhookSuccess++
	} else {
		c.webhookFailure++
	}
	c.mu.Unlock()
}

func (c *Collector) StartExecution(name string) {
	c.mu.Lock()
	c.executionStarts[name] = time.Now()
	c.mu.Unlock()
}

func (c *Collector) EndExecution(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	start, ok := c.executionStarts[name]
	if !ok {
		return
	}
	delete(c.executionStarts, name)
	times := append(c.executionTimes[name], time.Since(start))
	if len(times) > maxHistory {
		times = times[len(times)-maxHistory:]
	}
	c.executionTimes[name] = times
}

// Summary is the periodic metrics report.
type Summary struct {
	UptimeSeconds          float64            `json:"uptime_seconds"`
	UptimeFormatted        string             `json:"uptime_formatted"`
	TotalAlerts            int                `json:"total_alerts"`
	AlertCounts            map[string]int     `json:"alert_counts"`
	AlertFrequencyPerHour  map[string]float64 `json:"alert_frequency_per_hour"`
	TotalErrors            int                `json:"total_errors"`
	ErrorCounts            map[string]int     `json:"error_counts"`
	WebhookTotalCalls      int                `json:"webhook_total_calls"`
	WebhookSuccess         int                `json:"webhook_success"`
	WebhookFailure         int                `json:"webhook_failure"`
	WebhookSuccessRate     float64            `json:"webhook_success_rate"`
	MonitorExecutionTimesS map[string]float64 `json:"monitor_execution_times"`
}

func (c *Collector) Summary() Summary {
	c.mu.Lock()
	defer c.mu.Unlock()
	uptime := time.Since(c.start).Seconds()
	s := Summary{
		UptimeSeconds:          uptime,
		UptimeFormatted:        format.Duration(uptime),
		AlertCounts:            map[string]int{},
		AlertFrequencyPerHour:  map[string]float64{},
		ErrorCounts:            map[string]int{},
		WebhookSuccess:         c.webhookSuccess,
		WebhookFailure:         c.webhookFailure,
		WebhookTotalCalls:      c.webhookSuccess + c.webhookFailure,
		WebhookSuccessRate:     100,
		MonitorExecutionTimesS: map[string]float64{},
	}
	for k, v := range c.alertCounts {
		s.AlertCounts[k] = v
		s.TotalAlerts += v
	}
	cutoff := time.Now().Add(-time.Hour)
	for k, stamps := range c.alertStamps {
		n := 0
		for _, t := range stamps {
			if t.After(cutoff) {
				n++
			}
		}
		s.AlertFrequencyPerHour[k] = float64(n)
	}
	for k, v := range c.errorCounts {
		s.ErrorCounts[k] = v
		s.TotalErrors += v
	}
	if s.WebhookTotalCalls > 0 {
		s.WebhookSuccessRate = float64(c.webhookSuccess) / float64(s.WebhookTotalCalls) * 100
	}
	for k, times := range c.executionTimes {
		if len(times) == 0 {
			continue
		}
		var total time.Duration
		for _, t := range times {
			total += t
		}
		s.MonitorExecutionTimesS[k] = total.Seconds() / float64(len(times))
	}
	return s
}
