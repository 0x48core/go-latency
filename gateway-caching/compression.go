package gatewaycaching

import (
	"compress/gzip"
	"net/http"
	"strings"
)

type gzipResponseWriter struct {
	Writer *gzip.Writer
	http.ResponseWriter
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	return g.Writer.Write(b)
}

func CompressionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if client accepts gzip
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		// Wrap response writer
		gz := gzip.NewWriter(w)
		defer gz.Close()

		w.Header().Set("Content-Encoding", "gzip")
		gzw := &gzipResponseWriter{Writer: gz, ResponseWriter: w}

		next.ServeHTTP(gzw, r)
	})
}
