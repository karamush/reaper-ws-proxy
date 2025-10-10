package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/olahol/melody"
)

var (
	reaperBaseURL = flag.String("reaper-url", "http://localhost:8088", "base URL of REAPER HTTP interface")
	reaperRCName  = flag.String("reaper-rc-name", "ws", "Name for rc.reaper.fm/NAME_HERE")
	pollKeys      = flag.String("poll-get-keys", "GET/EXTSTATE/DRTUX/need_refresh;TRANSPORT", "comma-separated keys/commands for poll from REAPER and push to WebSocket")
	pollInterval  = flag.Duration("poll-interval", 80*time.Millisecond, "interval between polls to REAPER")
	listenAddr    = flag.String("addr", ":8090", "address to listen on")
	wsPath        = flag.String("ws-path", "/ws", "websocket path")
	wwwRootPath   = flag.String("www-root-path", "./www", "path to serve static files from")
	healthPath    = flag.String("health-path", "/health", "health check HTTP path")
)

func main() {
	flag.Parse()

	m := melody.New()

	mux := http.NewServeMux()

	mux.HandleFunc(*wsPath, func(w http.ResponseWriter, r *http.Request) {
		// Проверка авторизации / Origin, токенов и т.д.
		err := m.HandleRequest(w, r)
		if err != nil {
			log.Println("WS handshake error:", err)
		}
	})

	m.HandleConnect(func(s *melody.Session) {
		log.Println("WS connected: ", s.Request.RemoteAddr)
		if state := getLastState(); state != nil {
			s.Write(state)
		}
	})

	m.HandleDisconnect(func(s *melody.Session) {
		log.Println("WS disconnected: ", s.Request.RemoteAddr)
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

		err := proxyHandler(w, r)
		if err != nil {
			log.Println("proxy error:", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	})

	mux.HandleFunc(*healthPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	go publishReaperRC()

	server := &http.Server{
		Addr:    *listenAddr,
		Handler: mux,
		// Можно настроить ReadTimeout, WriteTimeout, IdleTimeout
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

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutdown signal received")

	close(stopCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Println("HTTP server Shutdown:", err)
	}

	m.Close()
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

func publishReaperRC() {
	if *reaperRCName == "" {
		log.Printf("No reaper-rc-name, SKIP!")
		return
	}

	// TODO: добавить поддержку rc.reaper.fm - добавлять именованный адрес с нужным IP и портом
	// curl -H "User-Agent: reaper_csurf_www/0.1" rc.reaper.fm/_/chords/192.168.93.250/8090
	ip, err := getLocalIP()
	if err != nil {
		log.Printf("Can't get local ip: " + err.Error())
		return
	}

	log.Printf("LOCAL IP: %s", ip)

	client := &http.Client{}

	_, port, _ := net.SplitHostPort(*listenAddr)
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
	respBody, _ := ioutil.ReadAll(resp.Body)

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
	ticker := time.NewTicker(*pollInterval)
	defer ticker.Stop()

	var lastBroadcastTime time.Time
	minBroadcastInterval := 10 * time.Millisecond

	for {
		select {
		case <-stopCh:
			log.Println("pollAndBroadcast exiting")
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
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
					// игнорируем ожидаемые случаи
				} else {
					log.Println("poll: request error:", err, "ctx.Err:", ctx.Err())
				}
				continue
			}
			data, err := ioutil.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				log.Println("poll: read error:", err)
				continue
			}
			cancel()

			old := getLastState()
			if old == nil || !equalBytes(old, data) {
				// состояние изменилось
				setLastState(data)

				now := time.Now()
				if now.Sub(lastBroadcastTime) >= minBroadcastInterval {
					// делаем рассылку
					err := m.Broadcast(data)
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
