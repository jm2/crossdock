// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package serve

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"
)

// statusWriter records the response status and size for access logs.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// AccessLog wraps h with one log line per request.
func AccessLog(h http.Handler, l *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		h.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		l.Printf("%s %s %q %d %d %s %q", host, r.Method, r.URL.RequestURI(), sw.status, sw.bytes,
			time.Since(start).Round(time.Millisecond), r.UserAgent())
	})
}

// Run serves h on addr until ctx is cancelled, then drains connections
// for up to 30 seconds.
func Run(ctx context.Context, addr string, h http.Handler, ready func(net.Addr)) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: large package downloads on slow links are normal.
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 16 << 10,
	}
	if ready != nil {
		ready(ln.Addr())
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
