package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/olahol/melody"
)

var (
	showVersion   = flag.Bool("version", false, "show version and exit")
	reaperBaseURL = flag.String("reaper-url", "http://localhost:8088", "base URL of REAPER HTTP interface")
	reaperRCName  = flag.String("reaper-rc-name", "ws", "name for rc.reaper.fm/NAME_HERE (disable if empty)")
	mDNSService   = flag.String("mdns-service", "REAPER", "name for mDNS service (disable mDNS if empty)")
	pollKeys      = flag.String("poll-get-keys", "TRANSPORT;GET/EXTSTATE/TUX/text;GET/EXTSTATE/TUX/need_refresh", "comma-separated keys/commands for poll from REAPER and push to WebSocket")
	pollInterval  = flag.Duration("poll-interval", 80*time.Millisecond, "interval between polls to REAPER")
	listenAddr    = flag.String("addr", ":8090", "address to listen on")
	wsPath        = flag.String("ws-path", "/ws", "websocket path")
	wwwRootPath   = flag.String("www-root-path", "./www", "path to serve static files from")
	healthPath    = flag.String("health-path", "/health", "health check HTTP path")
)

var (
	commit  = "none"
	version = "dev"
	date    = "unknown"
)

func main() {
	flag.Usage = func() {
		appName := filepath.Base(os.Args[0])
		_, _ = fmt.Fprintf(os.Stderr, "WebSocket proxy server for REAPER\n")
		printVersion(true)
		_, _ = fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", appName)
		flag.PrintDefaults()
	}
	flag.Parse()

	if showVersion != nil && *showVersion {
		printVersion(true)
		return
	}

	printVersion(false)

	m := melody.New()

	mux := http.NewServeMux()

	mux.HandleFunc(*wsPath, func(w http.ResponseWriter, r *http.Request) {
		err := m.HandleRequest(w, r)
		if err != nil {
			log.Println("WS handshake error:", err)
		}
	})

	m.HandleConnect(func(s *melody.Session) {
		log.Printf("WS connected: %s - %s", s.Request.RemoteAddr, s.Request.UserAgent())
		if state := getLastState(); state != nil {
			_ = s.Write(state)
		}
	})

	m.HandleDisconnect(func(s *melody.Session) {
		log.Printf("WS disconnected: %s - %s", s.Request.RemoteAddr, s.Request.UserAgent())
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		reqPath := r.URL.Path
		var tryPaths []string

		// 1. Точный путь, как есть
		tryPaths = append(tryPaths, filepath.Join(*wwwRootPath, filepath.FromSlash(path.Clean(reqPath))))
		// 2. Путь + .html (если в reqPath нет расширения)
		if filepath.Ext(reqPath) == "" {
			tryPaths = append(tryPaths, filepath.Join(*wwwRootPath, filepath.FromSlash(path.Clean(reqPath)))+".html")
		}
		// 3. Путь + /index.html
		tryPaths = append(tryPaths, filepath.Join(*wwwRootPath, filepath.FromSlash(path.Clean(reqPath)), "index.html"))

		for _, p := range tryPaths {
			if !strings.HasPrefix(p, filepath.Clean(*wwwRootPath)+string(os.PathSeparator)) {
				continue
			}
			info, err := os.Stat(p)
			if err == nil && !info.IsDir() {
				serveFile(w, r, p)
				return
			}
		}

		// Ничего не найдено, значит, просто проксируем запрос дальше до рипера :)
		err := proxyHandler(w, r)
		if err != nil {
			log.Println("proxy error:", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	})

	mux.HandleFunc(*healthPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	go publishReaperRC()

	server := &http.Server{
		Addr:         *listenAddr,
		Handler:      permissionsPolicyMiddleware(mux),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	stopCh := make(chan struct{})

	go pollAndBroadcast(m, stopCh)

	go func() {
		log.Printf("Listening on %s", *listenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// mDNS
	mDNSServer, err := createMDNSServer()
	if err != nil {
		log.Printf("mDNS server error: " + err.Error())
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutdown signal received")

	if mDNSServer != nil {
		log.Println("Stopping mDNS server...")
		mDNSServer.Shutdown()
		log.Println("mDNS server stopped")
	}

	close(stopCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Println("HTTP server Shutdown:", err)
	}

	_ = m.Close()
}

func permissionsPolicyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Permissions-Policy", `screen-wake-lock=(self)`)
		next.ServeHTTP(w, r)
	})
}

func printVersion(printCommit bool) {
	if printCommit {
		fmt.Printf("Version: %s, commit: %s, built at: %s\n", version, commit, date)
		return
	}

	fmt.Printf("Version: %s, built at: %s\n", version, date)
}

func serveFile(w http.ResponseWriter, r *http.Request, filePath string) {
	f, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "file open error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	http.ServeContent(w, r, filePath, getModTime(f), f)
}

func getModTime(f *os.File) (modtime time.Time) {
	info, err := f.Stat()
	if err != nil {
		return time.Now()
	}
	return info.ModTime()
}

func createMDNSServer() (*zeroconf.Server, error) {
	if *mDNSService == "" {
		log.Printf("No service name for mDNS, skipping!")
		return nil, nil
	}

	ip, port, _ := getLocalIPAndPort()
	portInt, _ := strconv.Atoi(port)

	txtInfo := []string{"Reaper Lyrics and Chords via reaper-ws-proxy (" + version + ")"}

	log.Printf("Starting mDNS server. Addr: %s:%d, ServiceName: %s", ip, portInt, *mDNSService)

	server, err := zeroconf.Register(*mDNSService, "_http._tcp", "local.", portInt, txtInfo, nil)
	if err != nil {
		return nil, err
	}
	return server, nil
}

func getLocalIPAndPort() (string, string, error) {
	ip, err := getLocalIP()
	if err != nil {
		log.Printf("Can't get local ip: " + err.Error())
		return "", "", err
	}
	_, port, _ := net.SplitHostPort(*listenAddr)

	return ip, port, nil
}

func publishReaperRC() {
	if *reaperRCName == "" {
		log.Printf("No reaper-rc-name, SKIP!")
		return
	}

	ip, port, err := getLocalIPAndPort()
	if err != nil {
		log.Printf("ReaperRC: can't get local ip, SKIP!")
		return
	}
	log.Printf("LOCAL IP: %s", ip)

	client := &http.Client{}

	uri := fmt.Sprintf("_/%s/%s/%s", *reaperRCName, ip, port)
	log.Printf("Sending %s to rc.reaper.fm", uri)
	req, _ := http.NewRequest("GET", "https://rc.reaper.fm/"+uri, nil)
	req.Header.Add("User-Agent", "reaper_csurf_www/0.1")
	resp, err := client.Do(req)

	if err != nil {
		log.Printf("rc.reaper.fm error: %s", err.Error())
		return
	}

	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	log.Printf("rc.reaper.fm response: %s", string(respBody))
}

func getLocalIP() (string, error) {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	defer conn.Close()
	if err != nil {
		log.Fatal(err)
	}
	localAddress := conn.LocalAddr().(*net.UDPAddr)
	return localAddress.IP.String(), nil
}

// Проксирование HTTP-запросов к REAPER с контекстом и таймаутом
func proxyHandler(w http.ResponseWriter, r *http.Request) error {
	target := *reaperBaseURL + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	// Контекст с таймаутом
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, r.Method, target, r.Body)
	if err != nil {
		return err
	}
	// Копируем основные заголовки
	for name, vals := range r.Header {
		for _, v := range vals {
			req.Header.Add(name, v)
		}
	}

	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Копируем заголовки ответа
	for name, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

// Переменные для хранения состояния
var (
	lastStateMu sync.RWMutex
	lastState   []byte
)

func getLastState() []byte {
	lastStateMu.RLock()
	defer lastStateMu.RUnlock()
	if lastState == nil {
		return nil
	}
	// возвращаем копию, чтобы не было гонок
	copyBuf := make([]byte, len(lastState))
	copy(copyBuf, lastState)
	return copyBuf
}

func setLastState(b []byte) {
	lastStateMu.Lock()
	defer lastStateMu.Unlock()
	lastState = make([]byte, len(b))
	copy(lastState, b)
}

func pollAndBroadcast(m *melody.Melody, stopCh <-chan struct{}) {
	if *pollKeys == "" {
		log.Printf("Empty poll-get-keys parameter! Auto poll DISABLED!")
		return
	}

	ticker := time.NewTicker(*pollInterval)
	defer ticker.Stop()

	var lastBroadcastTime time.Time
	minBroadcastInterval := 10 * time.Millisecond

	var cancelRequest context.CancelFunc
	defer func() {
		if cancelRequest != nil {
			cancelRequest()
		}
	}()

	for {
		select {
		case <-stopCh:
			log.Println("pollAndBroadcast exiting")
			if cancelRequest != nil {
				cancelRequest()
			}
			return
		case <-ticker.C:
			if cancelRequest != nil { // Если предыдущий запрос ещё почему-то выполняется, прервём его
				cancelRequest()
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			cancelRequest = cancel

			url := *reaperBaseURL + "/_/" + *pollKeys
			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				cancel()
				log.Println("poll: new request error:", err)
				continue
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				cancel()
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				} else {
					log.Println("poll: request error:", err, "ctx.Err:", ctx.Err())
					time.Sleep(time.Second)
				}
				continue
			}
			data, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				log.Println("poll: read error:", err)
				continue
			}
			cancel()

			old := getLastState()
			if old == nil || !equalBytes(old, data) {
				diffData := getChangedData(old, data)

				setLastState(data) // Сохраняем всё свежее

				now := time.Now()
				if now.Sub(lastBroadcastTime) >= minBroadcastInterval {
					err := m.Broadcast(diffData)
					if err != nil {
						log.Println("poll: broadcast error:", err)
					}
					lastBroadcastTime = now
				} else {
					// если слишком рано — можно запланировать задержку или просто пропустить
					// (следующий тик скорее всего рассылка будет допустима)
				}
			}
		}
	}
}

func splitLines(b []byte) [][]byte {
	if b == nil || len(b) == 0 {
		return nil
	}
	lines := bytes.Split(b, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func getChangedData(old, new []byte) []byte {
	newLines := splitLines(new)
	oldLines := splitLines(old)

	var changed [][]byte
	if old == nil || len(oldLines) == 0 {
		for _, l := range newLines {
			changed = append(changed, l)
		}
	} else {
		maxLen := len(newLines)
		for i := 0; i < maxLen; i++ {
			var oldLine []byte
			if i < len(oldLines) {
				oldLine = oldLines[i]
			}
			if !bytes.Equal(newLines[i], oldLine) {
				changed = append(changed, newLines[i])
			}
		}
	}

	if len(changed) == 0 {
		return nil
	}

	return bytes.Join(changed, []byte("\n"))
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
