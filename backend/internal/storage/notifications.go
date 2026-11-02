package storage

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// Notification is one alert the monitor raised, whether or not it went anywhere.
type Notification struct {
	ID       int64           `json:"id"`
	TS       float64         `json:"ts"`
	Area     string          `json:"area"`
	Severity string          `json:"severity"`
	Title    string          `json:"title"`
	Body     string          `json:"body"`
	Fields   json.RawMessage `json:"fields"`
	Subject  string          `json:"subject,omitempty"`
	// Status is sent, failed, held (for the end of quiet hours), digest (sent in that summary),
	// muted, or kept (no channel takes it).
	Status string `json:"status"`
	// Channels are where it was delivered, or where it will go once held.
	Channels []string `json:"channels"`
	Error    string   `json:"error,omitempty"`
	Read     bool     `json:"read"`
}

// NotificationQuery filters the notification history. Zero values match everything.
type NotificationQuery struct {
	Search     string
	Severities []string
	Area       string
	Unread     bool
	Before     int64 // only notifications older than this ID, for paging
	Limit      int
}

// NotificationCounts sums up what is unread.
type NotificationCounts struct {
	Unread int `json:"unread"`
	// Alerts are unread warnings and criticals; the rest is information and recoveries.
	Alerts int    `json:"alerts"`
	Worst  string `json:"worst,omitempty"`
	Latest int64  `json:"latest"`
}

const notificationColumns = "id, ts, area, severity, title, body, fields, subject, status, channels, error, read"

// AddNotification stores a notification and returns its ID (0 when the store is unavailable).
func (s *Store) AddNotification(n Notification) int64 {
	channels, _ := json.Marshal(nonNil(n.Channels))
	if len(n.Fields) == 0 {
		n.Fields = json.RawMessage("[]")
	}
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return 0
	}
	res, err := db.Exec(`INSERT INTO notifications (ts, area, severity, title, body, fields, subject, status, channels, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.TS, n.Area, n.Severity, n.Title, n.Body, string(n.Fields), n.Subject, n.Status, string(channels), n.Error)
	if err != nil {
		log.Warn("Notification insert failed", "err", err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// SetNotificationStatus records how delivering a notification went.
func (s *Store) SetNotificationStatus(id int64, status string, channels []string, errText string) {
	list, _ := json.Marshal(nonNil(channels))
	if err := s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE notifications SET status = ?, channels = ?, error = ? WHERE id = ?", status, string(list), errText, id)
		return err
	}); err != nil && err != ErrUnavailable {
		log.Warn("Notification update failed", "err", err)
	}
}

// Notifications returns the newest notifications matching q, and how many match in all.
func (s *Store) Notifications(q NotificationQuery) ([]Notification, int) {
	where, args := q.where()
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil, 0
	}
	var total int
	db.QueryRow("SELECT COUNT(*) FROM notifications"+where, args...).Scan(&total)
	paged, pagedArgs := where, args
	if q.Before > 0 {
		paged = joinWhere(paged, "id < ?")
		pagedArgs = append(append([]any{}, args...), q.Before)
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query("SELECT "+notificationColumns+" FROM notifications"+paged+" ORDER BY id DESC LIMIT ?", append(pagedArgs, limit)...)
	if err != nil {
		log.Warn("Notification query failed", "err", err)
		return nil, total
	}
	return scanNotifications(rows), total
}

func (q NotificationQuery) where() (string, []any) {
	var clauses []string
	var args []any
	if q.Search != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q.Search) + "%"
		clauses = append(clauses, `(title LIKE ? ESCAPE '\' OR body LIKE ? ESCAPE '\' OR fields LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	if len(q.Severities) > 0 {
		clauses = append(clauses, "severity IN (?"+strings.Repeat(", ?", len(q.Severities)-1)+")")
		for _, s := range q.Severities {
			args = append(args, s)
		}
	}
	if q.Area != "" {
		clauses = append(clauses, "area = ?")
		args = append(args, q.Area)
	}
	if q.Unread {
		clauses = append(clauses, "read = 0")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func joinWhere(where, clause string) string {
	if where == "" {
		return " WHERE " + clause
	}
	return where + " AND " + clause
}

// HeldNotifications returns the notifications waiting for quiet hours to end, oldest first.
func (s *Store) HeldNotifications() []Notification {
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return nil
	}
	rows, err := db.Query("SELECT " + notificationColumns + " FROM notifications WHERE status = 'held' ORDER BY id")
	if err != nil {
		log.Warn("Held notification query failed", "err", err)
		return nil
	}
	return scanNotifications(rows)
}

func scanNotifications(rows *sql.Rows) []Notification {
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var fields, channels string
		if rows.Scan(&n.ID, &n.TS, &n.Area, &n.Severity, &n.Title, &n.Body, &fields, &n.Subject, &n.Status, &channels, &n.Error, &n.Read) != nil {
			continue
		}
		n.Fields = json.RawMessage(fields)
		json.Unmarshal([]byte(channels), &n.Channels)
		n.Channels = nonNil(n.Channels)
		out = append(out, n)
	}
	return out
}

// MarkNotificationsRead marks the given notifications (or, with all, every one) read or unread.
func (s *Store) MarkNotificationsRead(ids []int64, all, read bool) error {
	return s.tx(func(tx *sql.Tx) error {
		if all {
			_, err := tx.Exec("UPDATE notifications SET read = ? WHERE read != ?", read, read)
			return err
		}
		for _, id := range ids {
			if _, err := tx.Exec("UPDATE notifications SET read = ? WHERE id = ?", read, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// NotificationCounts sums up the unread notifications.
func (s *Store) NotificationCounts() NotificationCounts {
	var c NotificationCounts
	db, unlock := s.lock()
	defer unlock()
	if db == nil {
		return c
	}
	var crit, warn int
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(severity = 'critical'), 0), COALESCE(SUM(severity = 'warning'), 0),
		(SELECT COALESCE(MAX(id), 0) FROM notifications) FROM notifications WHERE read = 0`).Scan(&c.Unread, &crit, &warn, &c.Latest)
	c.Alerts = crit + warn
	switch {
	case crit > 0:
		c.Worst = "critical"
	case warn > 0:
		c.Worst = "warning"
	case c.Unread > 0:
		c.Worst = "info"
	}
	return c
}

// PruneNotifications deletes notifications older than the cutoff, keeping held ones.
func (s *Store) PruneNotifications(olderThan float64) {
	if err := s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM notifications WHERE ts < ? AND status != 'held'", olderThan)
		return err
	}); err != nil && err != ErrUnavailable {
		log.Warn("Notification prune failed", "err", err)
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
