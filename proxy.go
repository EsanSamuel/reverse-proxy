package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

var backends = []string{
	"http://localhost:8080",
	"http://localhost:8081",
}

var userBackends = make(map[string]*url.URL)

var rrIndex uint32
var healthyBackends []string
var mu sync.RWMutex

type responseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
}

func NewResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
	}
}

func (rw *responseWriter) WriteStatusCode(code int) {
	rw.statusCode = code
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	if err != nil {
		fmt.Println("Error writing byte", err)
		return 0, err
	}
	return n, nil
}

func main() {
	mu.Lock()
	healthyBackends = append([]string(nil), backends...)
	mu.Unlock()

	go healthCheckRoutine()

	http.HandleFunc("/", proxyHandler)
	http.HandleFunc("/register", registerUserBackend)
	fmt.Println("Proxy server is running at port 9000")
	if err := http.ListenAndServe(":9000", nil); err != nil {
		fmt.Println("Proxy server failed to connect ", err)
	}
}

func getNextBackendUrl() string {
	mu.RLock()
	defer mu.RUnlock()

	n := len(healthyBackends)
	if n == 0 {
		return ""
	}

	idx := atomic.AddUint32(&rrIndex, 1)
	target := backends[int(idx-1)%n]

	return target
}

func registerUserBackend(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	backendUrl := r.URL.Query().Get("url")

	if name == "" || backendUrl == "" {
		http.Error(w, "No name or url found", http.StatusInternalServerError)
		return
	}

	parsedUrl, err := url.Parse(backendUrl)
	fmt.Println(parsedUrl)

	if err != nil {
		log.Println("Error parsing url", err)
	}

	mu.Lock()
	userBackends[name] = parsedUrl
	mu.Unlock()
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")

	if name == "" {
		backendUrl := getNextBackendUrl()

		if backendUrl == "" {
			http.Error(w, "No url found", http.StatusInternalServerError)
		}

		backendURL, err := url.Parse(backendUrl)
		fmt.Println(backendURL)
		if err != nil {
			http.Error(w, "Bad backend URL", http.StatusInternalServerError)
			return
		}

		log.Printf("Proxying request %s %s -> %s", r.Method, r.URL.Path, backendURL)

		proxy := httputil.NewSingleHostReverseProxy(backendURL)

		r.Host = backendURL.Host

		start := time.Now()

		rw := NewResponseWriter(w)

		proxy.ServeHTTP(rw, r)
		duration := time.Since(start)

		log.Printf(
			"method=%s path=%s status=%d bytes=%d duration_ms=%dms",
			r.Method,
			r.URL.Path,
			rw.statusCode,
			rw.bytesWritten,
			duration.Milliseconds(),
		)
	} else {
		mu.Lock()
		backendURL, ok := userBackends[name]
		mu.Unlock()

		checkUserRegisteredUrlHealth(name)
		if !ok {
			http.Error(w, "Bad backend URL", http.StatusInternalServerError)
			return
		}

		log.Printf("Proxying request %s %s -> %s", r.Method, r.URL.Path, backendURL)

		proxy := httputil.NewSingleHostReverseProxy(backendURL)

		r.Host = backendURL.Host

		start := time.Now()

		rw := NewResponseWriter(w)

		proxy.ServeHTTP(rw, r)
		duration := time.Since(start)

		log.Printf(
			"method=%s path=%s status=%d bytes=%d duration_ms=%dms",
			r.Method,
			r.URL.Path,
			rw.statusCode,
			rw.bytesWritten,
			duration.Milliseconds(),
		)
	}
}

func healthCheckRoutine() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		newHealthy := []string{}

		for _, backend := range backends {
			if isBackendHealthy(backend) {
				newHealthy = append(newHealthy, backend)
			}
		}
		mu.Lock()
		healthyBackends = newHealthy
		mu.Unlock()

		log.Printf("Healthy backends: %v", newHealthy)
	}
}

func checkUserRegisteredUrlHealth(name string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		newHealthy := []string{}

		if isUserRegisteredBackendHealthy(userBackends[name]) {
			newHealthy = append(newHealthy, userBackends[name].String())
		}

		mu.Lock()
		healthyBackends = newHealthy
		mu.Unlock()

		log.Printf("Healthy backends: %v", newHealthy)
	}
}

// Simple HTTP GET health check
func isBackendHealthy(backend string) bool {
	client := http.Client{
		Timeout: 2 * time.Second,
	}
	resp, err := client.Get(backend)
	if err != nil {
		log.Printf("Backend down: %s (%v)", backend, err)
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

func isUserRegisteredBackendHealthy(backend *url.URL) bool {
	client := http.Client{
		Timeout: 2 * time.Second,
	}
	resp, err := client.Get(backend.String())
	if err != nil {
		log.Printf("Backend down: %s (%v)", backend, err)
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}
