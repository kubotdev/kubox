package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	role := env("DEMO_ROLE", "orders")
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		env("POSTGRES_HOST", "127.0.0.1"),
		env("POSTGRES_PORT", "5432"),
		env("POSTGRES_USER", "shop"),
		env("POSTGRES_PASSWORD", "shop"),
		env("POSTGRES_DB", "shop"),
	)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(10)
	db.SetConnMaxLifetime(time.Hour)

	init := ordersSchema
	if role == "inventory" {
		init = inventorySchema
	}
	waitReady(db, init)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	if role == "inventory" {
		mux.HandleFunc("/inventory", handleInventoryList(db))
		mux.HandleFunc("/inventory/reserve", handleReserve(db))
	} else {
		mux.HandleFunc("/orders", handleOrders(db))
	}

	addr := ":" + env("PORT", "8080")
	log.Printf("%s api listening on %s", role, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func waitReady(db *sql.DB, schema func(*sql.DB) error) {
	for {
		if err := db.Ping(); err != nil {
			log.Printf("waiting for postgres: %v", err)
		} else if err := schema(db); err == nil {
			return
		} else {
			log.Printf("schema init: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func ordersSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS orders (
		id SERIAL PRIMARY KEY,
		sku TEXT NOT NULL,
		qty INTEGER NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	return err
}

func inventorySchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS stock (
		sku TEXT PRIMARY KEY,
		stock INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	for _, sku := range []string{"WIDGET", "GADGET", "GIZMO"} {
		if _, err := db.Exec(
			`INSERT INTO stock (sku, stock) VALUES ($1, 1000000)
			 ON CONFLICT (sku) DO NOTHING`, sku,
		); err != nil {
			return err
		}
	}
	return nil
}

type order struct {
	ID     int64     `json:"id"`
	SKU    string    `json:"sku"`
	Qty    int       `json:"qty"`
	At     time.Time `json:"created_at"`
	Status string    `json:"status,omitempty"`
}

type createdOrder struct {
	ID     int64  `json:"id"`
	SKU    string `json:"sku"`
	Qty    int    `json:"qty"`
	Status string `json:"status"`
}

type stockRow struct {
	SKU   string `json:"sku"`
	Stock int    `json:"stock"`
}

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func method(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method != want {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func handleOrdersList(db *sql.DB, w http.ResponseWriter) {
	rows, err := db.Query(`SELECT id, sku, qty, created_at FROM orders ORDER BY id DESC LIMIT 50`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := []order{}
	for rows.Next() {
		var o order
		if err := rows.Scan(&o.ID, &o.SKU, &o.Qty, &o.At); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, out)
}

func handleOrdersCreate(db *sql.DB, w http.ResponseWriter, r *http.Request) {
	var req struct {
		SKU string `json:"sku"`
		Qty int    `json:"qty"`
	}
	if err := decode(r, &req); err != nil || req.SKU == "" || req.Qty < 1 {
		writeErr(w, http.StatusBadRequest, "expected {sku, qty} with qty >= 1")
		return
	}

	invURL := env("INVENTORY_URL", "http://inventory:8080") + "/inventory/reserve"
	payload, _ := json.Marshal(req)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(invURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "inventory service unreachable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		writeErr(w, http.StatusConflict, "insufficient stock")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeErr(w, http.StatusBadGateway, "inventory reservation failed")
		return
	}

	var id int64
	err = db.QueryRow(`INSERT INTO orders (sku, qty) VALUES ($1, $2) RETURNING id`, req.SKU, req.Qty).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}
	writeJSON(w, http.StatusCreated, createdOrder{ID: id, SKU: req.SKU, Qty: req.Qty, Status: "confirmed"})
}

func handleOrders(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleOrdersList(db, w)
		case http.MethodPost:
			handleOrdersCreate(db, w, r)
		default:
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleInventoryList(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !method(w, r, http.MethodGet) {
			return
		}
		rows, err := db.Query(`SELECT sku, stock FROM stock ORDER BY sku`)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "query failed")
			return
		}
		defer rows.Close()
		out := []stockRow{}
		for rows.Next() {
			var s stockRow
			if err := rows.Scan(&s.SKU, &s.Stock); err != nil {
				writeErr(w, http.StatusInternalServerError, "scan failed")
				return
			}
			out = append(out, s)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleReserve(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !method(w, r, http.MethodPost) {
			return
		}
		var req struct {
			SKU string `json:"sku"`
			Qty int    `json:"qty"`
		}
		if err := decode(r, &req); err != nil || req.SKU == "" || req.Qty < 1 {
			writeErr(w, http.StatusBadRequest, "expected {sku, qty} with qty >= 1")
			return
		}
		res, err := db.Exec(
			`UPDATE stock SET stock = stock - $1 WHERE sku = $2 AND stock >= $1`,
			req.Qty, req.SKU,
		)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "update failed")
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			writeErr(w, http.StatusConflict, "insufficient stock")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sku":      req.SKU,
			"qty":      req.Qty,
			"reserved": true,
		})
	}
}
