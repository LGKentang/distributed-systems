// payment-service is the downstream service in the chain:
//
//     order-service ---> payment-service ---> postgres
//
// It exposes a tiny HTTP API. The interesting part for learning is that its
// behaviour is configurable through environment variables so you can make it
// SLOW or FLAKY on purpose, then watch what that does to the upstream
// order-service (which calls it synchronously and, on purpose, has no
// timeouts or retries yet).
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/lib/pq"
)

// config is read once at startup from the environment. Change these values in
// docker-compose.yml and restart the service to change its behaviour.
type config struct {
	port string

	// failRate is the probability [0.0, 1.0] that a payment is randomly
	// declined with HTTP 500. Use it to simulate an unreliable downstream.
	failRate float64

	// delay is how long the service sleeps before responding. Use it to
	// simulate a slow downstream and watch the caller block.
	delay time.Duration

	dbURL string
}

func loadConfig() config {
	return config{
		port:     getenv("PORT", "8081"),
		failRate: getenvFloat("FAIL_RATE", 0.0),
		delay:    time.Duration(getenvInt("DELAY_MS", 0)) * time.Millisecond,
		dbURL:    getenv("DATABASE_URL", "postgres://app:app@postgres:5432/app?sslmode=disable"),
	}
}

var db *sql.DB

func main() {
	cfg := loadConfig()
	log.Printf("payment-service starting: port=%s fail_rate=%.2f delay=%s", cfg.port, cfg.failRate, cfg.delay)

	db = mustConnect(cfg.dbURL)
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/payments", handlePayment(cfg))

	addr := ":" + cfg.port
	log.Printf("payment-service listening on %s", addr)
	// Note: http.ListenAndServe uses a server with NO read/write timeouts by
	// default. That is one of the "real-world problems" left in on purpose.
	log.Fatal(http.ListenAndServe(addr, logRequests(mux)))
}

// paymentRequest is what order-service sends us.
type paymentRequest struct {
	OrderID     int `json:"order_id"`
	AmountCents int `json:"amount_cents"`
}

// paymentResponse is what we send back.
type paymentResponse struct {
	PaymentID int    `json:"payment_id"`
	Outcome   string `json:"outcome"` // "approved" or "declined"
}

func handlePayment(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}

		var req paymentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}

		// 1. Simulate a slow downstream. The caller is blocked this whole time.
		if cfg.delay > 0 {
			log.Printf("payment for order=%d sleeping for %s (simulated slowness)", req.OrderID, cfg.delay)
			time.Sleep(cfg.delay)
		}

		// 2. Simulate a flaky downstream: randomly fail before doing any work.
		if rand.Float64() < cfg.failRate {
			log.Printf("payment for order=%d RANDOMLY FAILED (fail_rate=%.2f)", req.OrderID, cfg.failRate)
			http.Error(w, "payment processor unavailable", http.StatusInternalServerError)
			return
		}

		// 3. Happy path: record an "approved" payment.
		var paymentID int
		err := db.QueryRow(
			`INSERT INTO payments (order_id, amount_cents, outcome) VALUES ($1, $2, 'approved') RETURNING id`,
			req.OrderID, req.AmountCents,
		).Scan(&paymentID)
		if err != nil {
			log.Printf("payment for order=%d DB error: %v", req.OrderID, err)
			http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		log.Printf("payment for order=%d APPROVED payment_id=%d amount_cents=%d", req.OrderID, paymentID, req.AmountCents)
		writeJSON(w, http.StatusOK, paymentResponse{PaymentID: paymentID, Outcome: "approved"})
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := db.Ping(); err != nil {
		http.Error(w, "db down: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "payment-service"})
}

// --- small shared helpers (kept here so each service is self-contained) ---

func mustConnect(dbURL string) *sql.DB {
	d, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("sql.Open: %v", err)
	}
	// Postgres may not be ready the instant this container starts, even with
	// compose's depends_on. Retry the first connection a handful of times.
	// This is a deliberately simple, hand-rolled retry — NOT a general retry
	// policy for live traffic (that is Challenge 5).
	for i := 0; i < 30; i++ {
		if err = d.Ping(); err == nil {
			log.Printf("connected to postgres")
			return d
		}
		log.Printf("waiting for postgres (%d/30): %v", i+1, err)
		time.Sleep(time.Second)
	}
	log.Fatalf("could not connect to postgres: %v", err)
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// logRequests wraps a handler and prints one line per request. Plain, grep-able
// logging on stdout — read it with `docker compose logs -f payment-service`.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s handled in %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

var _ = fmt.Sprintf // keep fmt imported for easy ad-hoc debugging
