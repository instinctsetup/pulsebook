// cmd/server runs a live matching engine with an embedded order-book
// dashboard: rest orders, hit the book with takers, or replay real Binance
// BTCUSDT aggressor flow and watch the ladder move.
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/instinctsetup/pulsebook/pb"
)

//go:embed web
var webFS embed.FS

type server struct {
	book      *pb.Book
	nextID    uint64
	rows      []pb.BinanceAggTrade
	lastOpNs  atomic.Int64
	replaying atomic.Bool
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *server) handleOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Side  string `json:"side"`
		Type  string `json:"type"` // limit | market
		Price int64  `json:"price"`
		Qty   int64  `json:"qty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	side := pb.Buy
	if body.Side == "sell" {
		side = pb.Sell
	}
	id := atomic.AddUint64(&s.nextID, 1)
	start := time.Now()
	var trades []pb.Trade
	var err error
	if body.Type == "market" {
		trades, err = s.book.AddMarket(id, side, body.Qty)
	} else {
		trades, err = s.book.AddLimit(id, side, body.Price, body.Qty)
	}
	s.lastOpNs.Store(time.Since(start).Nanoseconds())
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{"id": id, "trades": trades})
}

func (s *server) handleDepth(w http.ResponseWriter, r *http.Request) {
	bids, asks := s.book.Depth(12)
	writeJSON(w, map[string]any{
		"bids": bids, "asks": asks,
		"trades":     s.book.RecentTrades(12),
		"stats":      s.book.Stats(),
		"last_op_ns": s.lastOpNs.Load(),
	})
}

func (s *server) handleReplay(w http.ResponseWriter, r *http.Request) {
	if len(s.rows) == 0 {
		http.Error(w, "no Binance data loaded", 503)
		return
	}
	if !s.replaying.CompareAndSwap(false, true) {
		writeJSON(w, map[string]bool{"already": true})
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > len(s.rows) {
		n = 20000
	}
	go func() {
		defer s.replaying.Store(false)
		step := len(s.rows) / n
		if step < 1 {
			step = 1
		}
		for i := 0; i+n <= len(s.rows); i += n {
			if _, err := pb.ReplayBinance(s.book, s.rows[i:i+n], 0.01, 1e-6, &s.nextID); err != nil {
				log.Printf("replay chunk: %v", err)
				return
			}
			time.Sleep(300 * time.Millisecond) // visible pace for the dashboard
		}
	}()
	writeJSON(w, map[string]any{"replaying": true, "chunk": n})
}

func (s *server) handleSeed(w http.ResponseWriter, r *http.Request) {
	go func() {
		mid := int64(82000_00)
		for i := int64(1); i <= 40; i++ {
			id := atomic.AddUint64(&s.nextID, 1)
			s.book.AddLimit(id, pb.Buy, mid-i*3, 20000+int64(i*3000))
			id = atomic.AddUint64(&s.nextID, 1)
			s.book.AddLimit(id, pb.Sell, mid+i*3, 20000+int64(i*3000))
		}
	}()
	writeJSON(w, map[string]bool{"seeding": true})
}

func main() {
	addr := flag.String("addr", ":8092", "listen address")
	data := flag.String("data", "data/BTCUSDT-aggTrades-2026-10-19.csv", "Binance aggTrades CSV")
	flag.Parse()
	s := &server{book: pb.NewBook()}
	if rows, err := pb.LoadBinanceAggTrades(*data, 500_000); err == nil {
		s.rows = rows
		log.Printf("loaded %s real Binance BTCUSDT aggTrades", commaInt(len(rows)))
	} else {
		log.Printf("no Binance data at %s (%v) - replay disabled", *data, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/order", s.handleOrder)
	mux.HandleFunc("GET /api/depth", s.handleDepth)
	mux.HandleFunc("POST /api/replay", s.handleReplay)
	mux.HandleFunc("POST /api/seed", s.handleSeed)
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	log.Printf("pulsebook dashboard on http://localhost%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func commaInt(n int) string {
	s := strconv.Itoa(n)
	var out []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}
