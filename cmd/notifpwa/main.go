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
	"runtime/debug"
	"syscall"
	"time"

	"notifpwa/internal/server"
)

// version is the build version, injected at build time via
//
//	-ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// When it is not stamped (e.g. `go run` or a plain `go build`), appVersion
// falls back to the VCS revision the Go toolchain embeds.
var version string

// appVersion resolves the version string to display: the stamped build version,
// else the embedded VCS commit (short, with a "-dirty" suffix when the working
// tree had uncommitted changes), else "dev".
func appVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		var rev string
		var dirty bool
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			if len(rev) > 7 {
				rev = rev[:7]
			}
			if dirty {
				return rev + "-dirty"
			}
			return rev
		}
	}
	return "dev"
}

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
		Version:    appVersion(),
	})
	if err != nil {
		log.Fatalf("start: %v", err)
	}
	defer srv.Close()

	log.Printf("notifpwa %s listening on :%s", appVersion(), port)
	if tok := srv.InitialToken(); tok != "" {
		log.Printf("Open the admin page: http://localhost:%s/admin?token=%s", port, tok)
		log.Printf("API token (send with 'Authorization: Bearer <token>'): %s", tok)
	} else {
		log.Printf("Open the admin page: http://localhost:%s/admin and sign in", port)
		log.Print("Admin tokens are already configured. Manage them in the admin panel, " +
			"or set API_TOKEN for a guaranteed root token.")
	}

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
