package relay

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 使用HTTP/1.1隔离每host空闲池的影响，避免HTTP/2多路复用掩盖连接关闭。
func TestForwardTLSConcurrentConnectionReuse(t *testing.T) {
	var opened atomic.Int32
	var arrived atomic.Int32
	var gate chan struct{}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if arrived.Add(1) == 8 {
			close(gate)
		}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "ok")
	}))
	provider.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	provider.StartTLS()
	defer provider.Close()
	s := NewServer(Config{UpstreamTimeout: 3 * time.Second}, nil)
	configured, ok := s.client.Transport.(*http.Transport)
	if !ok || configured == http.DefaultTransport {
		t.Fatal("NewServer must provide an independent HTTP transport")
	}
	transport := configured.Clone()
	transport.TLSClientConfig = provider.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.ForceAttemptHTTP2 = false
	s.client.Transport = transport
	defer transport.CloseIdleConnections()
	round := func() {
		arrived.Store(0)
		gate = make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resp, err := s.client.Get(provider.URL)
				if err != nil {
					t.Error(err)
					return
				}
				defer resp.Body.Close()
				if _, err = io.Copy(io.Discard, resp.Body); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	round()
	first := opened.Load()
	time.Sleep(100 * time.Millisecond)
	round()
	second := opened.Load() - first
	t.Logf("SIMULATED TLS HTTP/1.1: first_round_connections=%d second_round_new_connections=%d reused=%d configured_max_idle_per_host=%d", first, second, 8-second, transport.MaxIdleConnsPerHost)
	if first != 8 {
		t.Fatalf("first round connections=%d", first)
	}
	if transport.MaxIdleConnsPerHost != 32 || second != 0 {
		t.Fatalf("upstream pool failed to reuse all 8 connections: max=%d second_round_new=%d", transport.MaxIdleConnsPerHost, second)
	}
}

// 真正经过HTTP网络边界，独立观察响应头、首事件和后续事件，禁止Recorder冒充时延证据。
func TestForwardSSEHTTPArrivalAndCancellation(t *testing.T) {
	canceled := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		wait := func(d time.Duration) bool {
			select {
			case <-time.After(d):
				return true
			case <-r.Context().Done():
				select {
				case canceled <- struct{}{}:
				default:
				}
				return false
			}
		}
		if !wait(220 * time.Millisecond) {
			return
		}
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		w.(http.Flusher).Flush()
		if !wait(180 * time.Millisecond) {
			return
		}
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\n")
		w.(http.Flusher).Flush()
		if !wait(2 * time.Second) {
			return
		}
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer provider.Close()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, provider.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		proxySSE(w, resp)
	}))
	defer relay.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, relay.URL, nil)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	headersAt := time.Since(start)
	reader := bufio.NewReader(resp.Body)
	next := func(want string) time.Duration {
		t.Helper()
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(line, want) {
				return time.Since(start)
			}
		}
	}
	first := next("first")
	second := next("second")
	t.Logf("SIMULATED HTTP: headers=%s first=%s second=%s gap=%s X-Accel-Buffering=%q", headersAt, first, second, second-first, resp.Header.Get("X-Accel-Buffering"))
	if first < 200*time.Millisecond || second-first < 140*time.Millisecond {
		t.Fatal("stream events did not retain independent upstream timing")
	}
	if first-headersAt < 140*time.Millisecond || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("response headers were buffered until first content")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("downstream cancellation did not reach upstream")
	}
}

func BenchmarkSafeCompletionFrame(b *testing.B) {
	for _, size := range []int{32, 1024, 16384} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			frame := []byte(`{"id":"probe","choices":[{"delta":{"content":"` + strings.Repeat("x", size) + `"}}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
			b.ReportAllocs()
			b.SetBytes(int64(len(frame)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, _, _, err := safeCompletionFrame(frame); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestForwardSSEDirectVersusRelay(t *testing.T) {
	const input = `{"model":"simulated-provider","messages":[{"role":"user","content":"same input"}],"reasoning_effort":"high","stream":true}`
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != input {
			t.Errorf("provider received different input: %q err=%v", body, err)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-time.After(80 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"same-output\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-time.After(30 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer provider.Close()
	server := NewServer(Config{UpstreamTimeout: 3 * time.Second}, nil)
	if transport, ok := server.client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, provider.URL, r.Body)
		resp, err := server.client.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		proxySSE(w, resp)
	}))
	defer relay.Close()
	measure := func(url string) (time.Duration, time.Duration) {
		t.Helper()
		start := time.Now()
		resp, err := http.Post(url, "application/json", strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		headers := time.Since(start)
		reader := bufio.NewReader(resp.Body)
		var first time.Duration
		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(line, "same-output") {
				first = time.Since(start)
			}
		}
		if first == 0 {
			t.Fatal("first content missing")
		}
		return headers, first
	}
	var directHeaders, directFirst, relayHeaders, relayFirst time.Duration
	for i := 0; i < 3; i++ {
		dh, df := measure(provider.URL)
		rh, rf := measure(relay.URL)
		directHeaders += dh
		directFirst += df
		relayHeaders += rh
		relayFirst += rf
	}
	if calls.Load() != 6 {
		t.Fatalf("calls=%d", calls.Load())
	}
	t.Logf("SIMULATED same input/high reasoning, 3 paired samples: direct_headers=%s relay_headers=%s direct_first=%s relay_first=%s relay_extra_first=%s", directHeaders/3, relayHeaders/3, directFirst/3, relayFirst/3, (relayFirst-directFirst)/3)
}

func TestForwardSSEHTTPHeartbeat(t *testing.T) {
	comment := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-comment:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, ": provider-private-diagnostic\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer provider.Close()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, provider.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		proxySSE(w, resp)
	}))
	defer relay.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, relay.URL, nil)
	responses := make(chan *http.Response, 1)
	failures := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			failures <- err
			return
		}
		responses <- resp
	}()
	var resp *http.Response
	select {
	case resp = <-responses:
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("response headers buffered until first event")
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("buffering header=%q", resp.Header.Get("X-Accel-Buffering"))
	}
	close(comment)
	lines := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		if err != nil {
			failures <- err
			return
		}
		lines <- line
	}()
	select {
	case line := <-lines:
		if line != ": keep-alive\n" {
			t.Fatalf("unsafe or missing heartbeat=%q", line)
		}
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("heartbeat was buffered or discarded")
	}
	cancel()
}
