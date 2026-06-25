// Command builder starts the local web GUI for building, running, and testing
// model-baked llama.cpp images.
//
//	go run ./cmd/builder            # serves http://127.0.0.1:8799 and opens it
//	go run ./cmd/builder -dir .     # project dir holding models/ and images/
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/yourorg/local-llm/internal/builder"
	"github.com/yourorg/local-llm/internal/catalog"
	"github.com/yourorg/local-llm/internal/server"
)

func main() {
	dir := flag.String("dir", ".", "project directory holding config/, models/, images/")
	addr := flag.String("addr", "127.0.0.1:8799", "address to listen on")
	open := flag.Bool("open", true, "open the UI in a browser on startup")
	host := flag.String("host", envOr("MODEL_HOST", "127.0.0.1"),
		"host where launched model containers are reachable (set host.docker.internal when the factory runs in a container)")
	web := flag.String("web", envOr("WEB_DIR", ""),
		"serve the UI from this directory instead of the embedded copy (dev hot-reload); empty = embedded")
	flag.Parse()

	cat, err := catalog.Load(*dir)
	if err != nil {
		log.Fatalf("load catalog: %v", err)
	}
	b, err := builder.New(*dir)
	if err != nil {
		log.Fatalf("init builder: %v", err)
	}
	srv, err := server.New(b, cat, *host, *web)
	if err != nil {
		log.Fatalf("init server: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	url := "http://" + *addr
	log.Printf("local-llm builder UI:  %s", url)
	log.Printf("  models:  %s", b.ModelsDir)
	log.Printf("  images:  %s", b.ImagesDir)
	log.Printf("  catalog: %s  (%d models)", cat.Path, len(cat.Models))
	log.Printf("  model host: %s", *host)
	if *web != "" {
		log.Printf("  UI (dev): serving from disk %s", *web)
	}

	if *open {
		go func() {
			time.Sleep(500 * time.Millisecond)
			openBrowser(url)
		}()
	}
	log.Fatal(httpSrv.ListenAndServe())
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("could not open browser automatically: %v", err)
	}
}
