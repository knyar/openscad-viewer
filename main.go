package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
)

//go:embed static/*
var staticFiles embed.FS

var (
	openscadBin string
	scadFile    string
	scadFileAbs string
	offData     []byte
	offMu       sync.RWMutex
	clients     = make(map[*websocket.Conn]bool)
	clientsMu   sync.Mutex
	upgrader    = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <openscad-binary> <file.scad>\n", os.Args[0])
		os.Exit(1)
	}

	openscadBin = os.Args[1]
	scadFile = os.Args[2]

	var err error
	scadFileAbs, err = filepath.Abs(scadFile)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := os.Stat(scadFile); os.IsNotExist(err) {
		log.Fatalf("File not found: %s", scadFile)
	}

	if _, err := renderToOFF(); err != nil {
		log.Printf("Initial render failed: %v", err)
	}

	go watchFile()

	staticFS, _ := fs.Sub(staticFiles, "static")
	http.Handle("/", http.FileServer(http.FS(staticFS)))

	http.HandleFunc("/api/model.off", handleOFF)
	http.HandleFunc("/ws", handleWebSocket)

	port := "8000"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	fmt.Printf("OpenSCAD Viewer running at http://localhost:%s\n", port)
	fmt.Printf("Using OpenSCAD: %s\n", openscadBin)
	fmt.Printf("Watching: %s\n", scadFileAbs)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func renderToOFF() (string, error) {
	var warning string
	log.Println("Rendering...")
	start := time.Now()

	tmpFile, err := os.CreateTemp("", "openscad-*.off")
	if err != nil {
		return warning, err
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	cmd := exec.Command(openscadBin, scadFile, "-o", tmpPath, "--backend=manifold", "--export-format=off")
	o, err := cmd.CombinedOutput()
	output := string(o)
	if err != nil {
		return warning, fmt.Errorf("openscad error (%w): %s", err, output)
	}

	// Collect warnings from OpenSCAD output, starting with "WARNING: "
	for _, line := range strings.Split(output, "\n") {
		if s, ok := strings.CutPrefix(line, "WARNING: "); ok {
			warning += s + "\n"
		}
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return warning, err
	}

	offMu.Lock()
	offData = data
	offMu.Unlock()

	log.Printf("Rendered in %v (%d bytes)", time.Since(start).Round(time.Millisecond), len(data))
	if warning != "" {
		warning = strings.TrimSpace(warning)
		log.Printf("WARNINGS:\n%s", warning)
	}
	return warning, nil
}

func handleOFF(w http.ResponseWriter, r *http.Request) {
	offMu.RLock()
	data := offData
	offMu.RUnlock()

	if data == nil {
		http.Error(w, "Model not rendered", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write(data)
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()

	clientsMu.Lock()
	clients[conn] = true
	clientsMu.Unlock()

	defer func() {
		clientsMu.Lock()
		delete(clients, conn)
		clientsMu.Unlock()
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

func watchFile() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	dir := filepath.Dir(scadFileAbs)
	if err := watcher.Add(dir); err != nil {
		log.Fatal(err)
	}

	var lastEvent time.Time
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if time.Since(lastEvent) < 100*time.Millisecond {
					continue
				}
				lastEvent = time.Now()
				log.Printf("File %s changed, re-rendering...", event.Name)
				warn, err := renderToOFF()
				if err != nil {
					log.Printf("Render error: %v", err)
				}
				notifyClients(warn, err)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("Watcher error:", err)
		}
	}
}

func notifyClients(warn string, err error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()

	var msg []byte
	if err != nil {
		msg, _ = json.Marshal(map[string]string{
			"type":    "error",
			"message": err.Error(),
		})
	} else {
		msg, _ = json.Marshal(map[string]string{
			"type":    "reload",
			"warning": warn,
		})
	}

	for conn := range clients {
		err := conn.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			conn.Close()
			delete(clients, conn)
		}
	}
}
