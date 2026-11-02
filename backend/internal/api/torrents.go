package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// qBittorrent: torrent actions, adding torrents, and categories.

// needQB answers 503 when qBittorrent monitoring is off.
func (s *Server) needQB(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.ctx.QBittorrent == nil || s.ctx.QBittorrentBoard == nil {
			writeJSON(w, http.StatusServiceUnavailable, errBody("qBittorrent monitoring is off"))
			return
		}
		next(w, r)
	}
}

func (s *Server) qbCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.ctx.QBittorrent.Categories()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": cats})
}

// qbAction starts, stops, reannounces, rechecks or deletes torrents, then refreshes the list so
// the dashboard shows the result at once.
func (s *Server) qbAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action      string   `json:"action"`
		Hashes      []string `json:"hashes"`
		DeleteFiles bool     `json:"delete_files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"action": "...", "hashes": ["..."]}`))
		return
	}
	qb := s.ctx.QBittorrent
	if err := qb.Apply(req.Action, req.Hashes, req.DeleteFiles); err != nil {
		writeJSON(w, http.StatusBadGateway, errBody(err.Error()))
		return
	}
	log.Info("qBittorrent action from the dashboard", "action", req.Action, "torrents", len(req.Hashes), "delete_files", req.DeleteFiles)
	s.refreshQB()
	writeJSON(w, http.StatusOK, map[string]any{"qbittorrent": s.ctx.QBittorrentBoard.View()})
}

func (s *Server) qbAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URLs     string `json:"urls"`
		Category string `json:"category"`
		Stopped  bool   `json:"stopped"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"urls": "magnet:?..."}`))
		return
	}
	var urls []string
	for _, line := range strings.Split(req.URLs, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			urls = append(urls, line)
		}
	}
	if err := s.ctx.QBittorrent.Add(urls, strings.TrimSpace(req.Category), req.Stopped); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	log.Info("Torrents added from the dashboard", "count", len(urls))
	// qBittorrent fetches magnet metadata in the background; give it a moment to list them.
	time.Sleep(500 * time.Millisecond)
	s.refreshQB()
	writeJSON(w, http.StatusCreated, map[string]any{"added": len(urls), "qbittorrent": s.ctx.QBittorrentBoard.View()})
}

func (s *Server) refreshQB() {
	if list, err := s.ctx.QBittorrent.List(); err == nil {
		s.ctx.QBittorrentBoard.Refresh(nowSeconds(), list)
	}
}
