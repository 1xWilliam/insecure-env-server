package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	defaultPort       = ":8080"
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 5 * time.Second
	writeTimeout      = 5 * time.Second
	idleTimeout       = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
	maxHeaderBytes    = 1024 * 16
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/{env}", getENV(logger))

	handler := recoverMiddleware(logger, mux)

	srv := &http.Server{
		Addr:              port,
		Handler:           handler,
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("starting server", "port", port)
		serverErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed to start", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		stop()
		logger.Info("shutdown signal received, draining connections")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed, forcing close", "error", err)
			_ = srv.Close()
			os.Exit(1)
		}
		logger.Info("server shutdown gracefully")
	}
}

func getENV(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			logger.Info("request",
				"remote_addr", r.RemoteAddr,
				"status", http.StatusMethodNotAllowed,
				"method", r.Method,
				"path", r.PathValue("env"),
			)
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
			return
		}

		env, exists := os.LookupEnv(r.PathValue("env"))
		if exists == false {
			logger.Info("request",
				"remote_addr", r.RemoteAddr,
				"status", http.StatusNotFound,
				"path", r.PathValue("env"),
			)
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}

		logger.Info("request",
			"remote_addr", r.RemoteAddr,
			"status", http.StatusOK,
			"path", r.PathValue("env"),
		)

		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(env))
		if err != nil {
			logger.Error("failed to write response", "error", err)
		}
	}
}

func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				logger.Error("panic recovered", "error", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
