// Command jaroserve is a demo server: GET /users returns 1000 users in the
// format and encoding the client asks for; POST /users validates the body.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/lukaszraczylo/jaro"
	"github.com/lukaszraczylo/jaro/jarohttp"
	"github.com/lukaszraczylo/jaro/zstdenc"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()
	zstdenc.Register()

	def, err := jaro.Unmarshal([]byte(`users:[{id:"int(1..)" name:"str(1..40)" score:num}]`))
	if err != nil {
		log.Fatal(err)
	}
	schema, err := jaro.Compile(def)
	if err != nil {
		log.Fatal(err)
	}
	rows := make([]any, 1000)
	for i := range rows {
		rows[i] = map[string]any{"id": int64(i + 1), "name": "user " + string(rune('a'+i%26)), "score": float64(i%9000) / 100}
	}
	data := map[string]any{"users": rows}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /users", func(w http.ResponseWriter, r *http.Request) { jarohttp.Write(w, r, http.StatusOK, data) })
	mux.Handle("POST /users", jarohttp.Validated(schema, 0, func(w http.ResponseWriter, r *http.Request, v any) {
		jarohttp.Write(w, r, http.StatusCreated, map[string]any{"ok": true})
	}))
	srv := &http.Server{Addr: *addr, Handler: jarohttp.Compress(0, mux), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
