// Command notifpwa runs the self-hosted Web Push PWA server.
package main

import (
	"log"
	"net/http"
	"os"

	"notifpwa/internal/server"
)

func main() {
	port := getenv("PORT", "8080")

	srv, err := server.New(server.Config{
		DBPath:     getenv("DB_PATH", "./data.db"),
		Subscriber: getenv("VAPID_SUBSCRIBER", "mailto:admin@localhost"),
	})
	if err != nil {
		log.Fatalf("start: %v", err)
	}
	defer srv.Close()

	log.Printf("notifpwa listening on :%s", port)
	log.Printf("Open the admin page: http://localhost:%s/admin?token=%s", port, srv.Token())
	log.Printf("API token (send with 'Authorization: Bearer <token>'): %s", srv.Token())

	if err := http.ListenAndServe(":"+port, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
