// Command notifpwa runs the self-hosted Web Push PWA server.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"notifpwa/internal/server"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe local /healthz and exit 0/1")
	flag.Parse()
	if *healthcheck {
		os.Exit(runHealthcheck(getenv("PORT", "8080")))
	}

	port := getenv("PORT", "8080")

	srv, err := server.New(server.Config{
		DBPath:     getenv("DB_PATH", "./data.db"),
		Subscriber: getenv("VAPID_SUBSCRIBER", "mailto:admin@localhost"),
		Token:      os.Getenv("API_TOKEN"),
	})
	if err != nil {
		log.Fatalf("start: %v", err)
	}
	defer srv.Close()

	log.Printf("notifpwa listening on :%s", port)
	log.Printf("Open the admin page: http://localhost:%s/admin?token=%s", port, srv.Token())
	log.Printf("API token (send with 'Authorization: Bearer <token>'): %s", srv.Token())

	httpSrv := &http.Server{Addr: ":" + port, Handler: srv.Handler()}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// runHealthcheck is used by the container HEALTHCHECK: it hits the local
// /healthz and maps the result to a process exit code.
func runHealthcheck(port string) int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return 0
	}
	return 1
}
