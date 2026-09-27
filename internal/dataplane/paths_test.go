package dataplane

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rpop-project/rpop/internal/snapshot"
)

// fakePaths dials the paths named "good*" directly and fails or breaks the others, counting attempts.
type fakePaths struct {
	mu       sync.Mutex
	attempts map[string]int
}

func (f *fakePaths) DialPath(ctx context.Context, path snapshot.Path) (net.Conn, error) {
	f.mu.Lock()
	if f.attempts == nil {
		f.attempts = make(map[string]int)
	}
	f.attempts[path.Label]++
	f.mu.Unlock()
	switch {
	case strings.HasPrefix(path.Label, "good"):
		return (&net.Dialer{}).DialContext(ctx, "tcp", path.Target)
	case strings.HasPrefix(path.Label, "broken"):
		client, server := net.Pipe()
		server.Close()
		return client, nil
	default:
		return nil, errors.New("link is down")
	}
}

func (f *fakePaths) count(label string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts[label]
}

func pathSite(t *testing.T, engine *Engine, upstream *httptest.Server, labels ...string) *httptest.Server {
	t.Helper()
	target := strings.TrimPrefix(upstream.URL, "http://")
	paths := make([]snapshot.Path, 0, len(labels))
	for _, label := range labels {
		paths = append(paths, snapshot.Path{Label: label, Target: target})
	}
	handler, err := engine.Handler(snapshot.Site{ID: "paths", Upstreams: []snapshot.Upstream{{URL: upstream.URL, Paths: paths}}})
	if err != nil {
		t.Fatal(err)
	}
	site := httptest.NewServer(handler)
	t.Cleanup(site.Close)
	return site
}

func TestFailoverUsesTheFirstPathThatConnects(t *testing.T) {
	engine := newTestEngine(t)
	dialer := &fakePaths{}
	engine.SetPathDialer(dialer)
	var bodies []string
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	site := pathSite(t, engine, upstream, "down", "good")

	response, err := http.Post(site.URL, "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("response %d %q", response.StatusCode, body)
	}
	mu.Lock()
	if len(bodies) != 1 || bodies[0] != "payload" {
		t.Fatalf("upstream received %q", bodies)
	}
	mu.Unlock()
	if _, err := http.Get(site.URL); err != nil {
		t.Fatal(err)
	}
	if got := dialer.count("down"); got != 1 {
		t.Fatalf("the failed path was dialed %d times; it should cool down after one failure", got)
	}
}

func TestFailoverNeverRetriesARequestThatReachedAPath(t *testing.T) {
	engine := newTestEngine(t)
	dialer := &fakePaths{}
	engine.SetPathDialer(dialer)
	upstream := textUpstream(t, "ok")
	site := pathSite(t, engine, upstream, "broken", "good")
	response, err := http.Post(site.URL, "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 from the path that connected and then failed", response.StatusCode)
	}
	if got := dialer.count("good"); got != 0 {
		t.Fatalf("a request that may have reached the upstream was sent again on another path (%d)", got)
	}
}

func TestPathsStreamServerSentEventsWithoutBuffering(t *testing.T) {
	engine := newTestEngine(t)
	engine.SetPathDialer(&fakePaths{})
	ack := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			fmt.Fprintf(w, "data: token-%d\n\n", i)
			w.(http.Flusher).Flush()
			if i < 2 {
				select {
				case <-ack:
				case <-r.Context().Done():
					return
				}
			}
		}
	}))
	defer upstream.Close()
	site := pathSite(t, engine, upstream, "down", "good")
	response, err := http.Get(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for i := range 3 {
		line := make(chan string, 1)
		go func() {
			for {
				text, err := reader.ReadString('\n')
				if err != nil || strings.HasPrefix(text, "data:") {
					line <- strings.TrimSpace(text)
					return
				}
			}
		}()
		select {
		case got := <-line:
			if got != fmt.Sprintf("data: token-%d", i) {
				t.Fatalf("event %d = %q", i, got)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("event %d was buffered", i)
		}
		if i < 2 {
			ack <- struct{}{}
		}
	}
}

// hangingPaths never connects the path labelled "hang" until the dial is abandoned.
type hangingPaths struct{ fakePaths }

func (h *hangingPaths) DialPath(ctx context.Context, path snapshot.Path) (net.Conn, error) {
	if path.Label == "hang" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return h.fakePaths.DialPath(ctx, path)
}

func TestFailoverBoundsHowLongOnePathMayTakeToConnect(t *testing.T) {
	engine := newTestEngine(t)
	engine.SetPathDialer(&hangingPaths{})
	upstream := textUpstream(t, "ok")
	target := strings.TrimPrefix(upstream.URL, "http://")
	f, transports := newPathTransports(http.DefaultTransport.(*http.Transport).Clone(),
		[]snapshot.Path{{Label: "hang", Target: target}, {Label: "good", Target: target}}, engine.pathDialer(), false)
	defer closeIdle(transports)
	f.establishTimeout = 100 * time.Millisecond
	request, _ := http.NewRequest(http.MethodGet, upstream.URL, nil)
	start := time.Now()
	response, err := f.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("falling back from a hanging path took %v", waited)
	}
}
