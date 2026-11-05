package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

var gzipPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression); return w }}

// compressible is what gzip helps: JSON polls and the text assets. Fonts and images are already compressed.
func compressible(contentType string) bool {
	for _, prefix := range []string{"application/json", "text/", "application/javascript", "image/svg+xml"} {
		if strings.HasPrefix(contentType, prefix) {
			return true
		}
	}
	return false
}

// gzipWriter decides at WriteHeader time whether to compress, from the handler's Content-Type.
type gzipWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (w *gzipWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	h := w.Header()
	h.Add("Vary", "Accept-Encoding")
	if code != http.StatusNoContent && code != http.StatusNotModified && h.Get("Content-Encoding") == "" && compressible(h.Get("Content-Type")) {
		h.Set("Content-Encoding", "gzip")
		h.Del("Content-Length")
		w.gz = gzipPool.Get().(*gzip.Writer)
		w.gz.Reset(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(b))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection, to extend a slow handler's write deadline.
func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipWriter) close() {
	if w.gz != nil {
		w.gz.Close()
		gzipPool.Put(w.gz)
	}
}

// compress gzips responses for clients that accept it. Range requests pass through untouched,
// since byte ranges refer to the uncompressed file.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}
