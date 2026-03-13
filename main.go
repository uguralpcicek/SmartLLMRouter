package main

import (
	"log"
	"net/http"

	"smartllmrouter/config"
	"smartllmrouter/router"
)

func main() {
	cfg, err := config.Load("config.json")
	if err != nil {
		log.Fatalf("can't load config: %v", err)
	}

	r := router.New(cfg)

	http.HandleFunc("/v1/chat/completions", r.Handle)

	// health check
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})

	log.Println("SmartLLMRouter listening on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}
