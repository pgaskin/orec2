package main

import (
	"net/http"
	"os"

	"github.com/a-h/templ"
	"github.com/pgaskin/orec2/schema"
	"google.golang.org/protobuf/proto"
)

//go:generate go tool templ generate

func main() {
	var data schema.Data
	if buf, err := os.ReadFile("data/data.pb"); err != nil {
		panic(err)
	} else if err := proto.Unmarshal(buf, &data); err != nil {
		panic(err)
	}

	http.Handle("GET /", templ.Handler(renderData(&data, false)))

	// go tool templ generate --watch --proxy="http://localhost:8080" --cmd="go run ./frontend"
	http.ListenAndServe(":8080", nil)
}
