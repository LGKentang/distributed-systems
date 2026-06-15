// order-service is the upstream service and the entry point for users:
//
//     you (browser/curl) ---> order-service ---> payment-service ---> postgres
//
// Creating an order makes a SYNCHRONOUS HTTP call to payment-service. That is
// the core teaching point of this playground: order-service is blocked while
// payment-service does its (possibly slow, possibly failing) work, and there
// are deliberately NO timeouts, retries, or circuit breakers around that call.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/lib/pq"
)

type config struct {
	port string

	// paymentURL is the base URL of payment-service. Note it uses the service
	// NAME "payment-service" as a hostname — that name is resolved by Docker's
	// embedded DNS server on the compose network (Challenge 2).
	paymentURL string

	dbURL string
}

func loadConfig() config {
	return config{
		port:       getenv("PORT", "8080"),
		paymentURL: getenv("PAYMENT_URL", "http://payment-service:8081"),
		dbURL:      getenv("DATABASE_URL", "postgres://app:app@postgres:5432/app?sslmode=disable"),
	}
}

var (
	db  *sql.DB
	cfg config
)

func main() {
	cfg = loadConfig()
	log.Printf("order-service starting: port=%s payment_url=%s", cfg.port, cfg.paymentURL)

	db = mustConnect(cfg.dbURL)
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)        // tiny web UI
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/orders", handleOrders) // GET list, POST create

	addr := ":" + cfg.port
	log.Printf("order-service listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, logRequests(mux)))
}

// --- types ---

type order struct {
	ID          int       `json:"id"`
	Item        string    `json:"item"`
	AmountCents int       `json:"amount_cents"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

type createOrderRequest struct {
	Item        string `json:"item"`
	AmountCents int    `json:"amount_cents"`
}

// these mirror payment-service's API.
type paymentRequest struct {
	OrderID     int `json:"order_id"`
	AmountCents int `json:"amount_cents"`
}
type paymentResponse struct {
	PaymentID int    `json:"payment_id"`
	Outcome   string `json:"outcome"`
}

// --- handlers ---

func handleOrders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		createOrder(w, r)
	case http.MethodGet:
		listOrders(w, r)
	default:
		http.Error(w, "use GET or POST", http.StatusMethodNotAllowed)
	}
}

func createOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Item == "" || req.AmountCents <= 0 {
		http.Error(w, "item is required and amount_cents must be > 0", http.StatusBadRequest)
		return
	}

	// 1. Persist the order as "pending".
	var o order
	o.Item = req.Item
	o.AmountCents = req.AmountCents
	o.Status = "pending"
	err := db.QueryRow(
		`INSERT INTO orders (item, amount_cents, status) VALUES ($1, $2, 'pending') RETURNING id, created_at`,
		o.Item, o.AmountCents,
	).Scan(&o.ID, &o.CreatedAt)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("order=%d created (pending), calling payment-service synchronously...", o.ID)

	// 2. Synchronously call payment-service. We BLOCK here. If payment-service
	//    is slow, this request is slow. If payment-service is down, this fails.
	//    There is intentionally no timeout and no retry around this call.
	outcome, err := callPayment(o.ID, o.AmountCents)
	if err != nil {
		// The downstream failed. We mark the order failed and report it.
		// Notice: the order row still exists as 'failed' — what *should*
		// happen to a half-completed order is a real design question for later.
		log.Printf("order=%d payment call FAILED: %v", o.ID, err)
		_, _ = db.Exec(`UPDATE orders SET status = 'failed' WHERE id = $1`, o.ID)
		o.Status = "failed"
		writeJSON(w, http.StatusBadGateway, o) // 502: the downstream let us down
		return
	}

	// 3. Payment succeeded — mark the order paid.
	_, _ = db.Exec(`UPDATE orders SET status = 'paid' WHERE id = $1`, o.ID)
	o.Status = "paid"
	log.Printf("order=%d PAID (payment outcome=%s)", o.ID, outcome)
	writeJSON(w, http.StatusCreated, o)
}

// callPayment makes the synchronous downstream HTTP request.
//
// It uses http.DefaultClient on purpose. The default client has NO timeout,
// so a hung payment-service hangs this call (and the user's request) forever.
// Fixing that is Challenge 4.
func callPayment(orderID, amountCents int) (string, error) {
	body, _ := json.Marshal(paymentRequest{OrderID: orderID, AmountCents: amountCents})

	resp, err := http.DefaultClient.Post(
		cfg.paymentURL+"/payments",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", err // DNS failure, connection refused, etc.
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return "", &downstreamError{Status: resp.StatusCode, Body: string(msg)}
	}

	var pr paymentResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return "", err
	}
	return pr.Outcome, nil
}

type downstreamError struct {
	Status int
	Body   string
}

func (e *downstreamError) Error() string {
	return "payment-service returned " + strconv.Itoa(e.Status) + ": " + e.Body
}

func listOrders(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT id, item, amount_cents, status, created_at FROM orders ORDER BY id DESC`)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	orders := []order{}
	for rows.Next() {
		var o order
		if err := rows.Scan(&o.ID, &o.Item, &o.AmountCents, &o.Status, &o.CreatedAt); err != nil {
			http.Error(w, "scan error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		orders = append(orders, o)
	}
	writeJSON(w, http.StatusOK, orders)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := db.Ping(); err != nil {
		http.Error(w, "db down: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "order-service"})
}

// handleIndex serves a deliberately tiny single-file web UI so you have a
// "frontend" without adding another container or any JS framework.
func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, indexHTML)
}

const indexHTML = `<!doctype html>
<html>
<head><meta charset="utf-8"><title>orders</title>
<style>
 body{font-family:system-ui,sans-serif;max-width:680px;margin:2rem auto;padding:0 1rem}
 input,button{font-size:1rem;padding:.4rem}
 table{border-collapse:collapse;width:100%;margin-top:1rem}
 td,th{border:1px solid #ccc;padding:.4rem .6rem;text-align:left}
 .paid{color:green}.failed{color:#b00}.pending{color:#a60}
</style></head>
<body>
 <h1>order-service</h1>
 <p>Create an order. order-service will synchronously call payment-service.</p>
 <form id="f">
   <input id="item" placeholder="item" value="coffee" required>
   <input id="amount" type="number" placeholder="amount (cents)" value="500" required>
   <button>Create order</button>
 </form>
 <p id="msg"></p>
 <table id="t"><thead><tr><th>id</th><th>item</th><th>cents</th><th>status</th><th>created</th></tr></thead><tbody></tbody></table>
<script>
async function load(){
  const r = await fetch('/orders'); const rows = await r.json();
  document.querySelector('#t tbody').innerHTML = rows.map(o =>
    '<tr><td>'+o.id+'</td><td>'+o.item+'</td><td>'+o.amount_cents+
    '</td><td class="'+o.status+'">'+o.status+'</td><td>'+new Date(o.created_at).toLocaleTimeString()+'</td></tr>'
  ).join('');
}
document.getElementById('f').onsubmit = async (e) => {
  e.preventDefault();
  const msg = document.getElementById('msg'); msg.textContent = 'creating (blocked on payment-service)...';
  const t0 = performance.now();
  const r = await fetch('/orders', {method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({item: item.value, amount_cents: Number(amount.value)})});
  const dt = Math.round(performance.now()-t0);
  msg.textContent = 'HTTP '+r.status+' in '+dt+' ms';
  load();
};
load();
</script>
</body></html>`

// --- shared helpers ---

func mustConnect(dbURL string) *sql.DB {
	d, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("sql.Open: %v", err)
	}
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
