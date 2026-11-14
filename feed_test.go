package k9o

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func feedFrame(id, typ, rec string) string {
	return fmt.Sprintf("id: %s\nevent: %s\ndata: {\"topic\":\"collections.t.%s\",\"eventType\":\"%s\",\"payload\":{\"id\":\"%s\"},\"occurredAt\":\"2026-10-27T10:20:38.969Z\",\"eventId\":\"%s\"}\n\n", id, typ, typ, typ, rec, id)
}

type feedServer struct {
	mu      sync.Mutex
	lastIDs []string // Last-Event-ID per connection
	handler func(n int, w http.ResponseWriter, r *http.Request)
	srv     *httptest.Server
}

func newFeedServer(t *testing.T, h func(n int, w http.ResponseWriter, r *http.Request)) *feedServer {
	fs := &feedServer{handler: h}
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			return
		}
		fs.mu.Lock()
		fs.lastIDs = append(fs.lastIDs, r.Header.Get("Last-Event-ID"))
		n := len(fs.lastIDs) - 1
		fs.mu.Unlock()
		fs.handler(n, w, r)
	}))
	t.Cleanup(fs.srv.Close)
	return fs
}

func (fs *feedServer) ids() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.lastIDs...)
}

func (fs *feedServer) client(t *testing.T) *Client {
	c, kerr := NewClient(ClientOpts{BaseURL: fs.srv.URL, ProjectSlug: "portal", APIKey: "k"})
	if kerr != nil {
		t.Fatalf("NewClient: %v", kerr)
	}
	return c
}

func feedOpts() SubscribeOptions {
	return SubscribeOptions{idleTimeout: 300 * time.Millisecond, retryDelay: 5 * time.Millisecond}
}

func sendSSE(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, s)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func recv(t *testing.T, ch <-chan RealtimeEvent) RealtimeEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("events closed")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
	}
	return RealtimeEvent{}
}

func TestFeedYieldsEventIDReconnectsAndDedupes(t *testing.T) {
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 0:
			sendSSE(w, "retry: 5\n:ok\n\n"+feedFrame("evt_A", "row_inserted", "r1")+":hb\n\n"+feedFrame("evt_B", "row_inserted", "r2"))
		case 1: // resends B (lookback), then a new one
			sendSSE(w, feedFrame("evt_B", "row_inserted", "r2")+feedFrame("evt_C", "row_inserted", "r3"))
			<-r.Context().Done()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := fs.client(t).Realtime.Subscribe(ctx, "collections.t.*", feedOpts())
	a, b, c := recv(t, events), recv(t, events), recv(t, events)
	if a.EventID != "evt_A" || b.EventID != "evt_B" || c.EventID != "evt_C" || a.EventType != "row_inserted" {
		t.Fatalf("got %+v %+v %+v", a, b, c)
	}
	ids := fs.ids()
	if len(ids) < 2 || ids[0] != "" || ids[1] != "evt_B" {
		t.Fatalf("Last-Event-ID per connection = %q", ids)
	}
}

func TestFeedResyncCallsHandlerAndDropsPosition(t *testing.T) {
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 0:
			sendSSE(w, feedFrame("evt_A", "row_inserted", "r1"))
		case 1:
			sendSSE(w, "event: resync\ndata: {\"reason\":\"cursor-unknown\",\"action\":\"reread-source\"}\n\n")
		default: // same id again: dedupe set was cleared, so it is delivered
			sendSSE(w, feedFrame("evt_A", "row_inserted", "r1"))
			<-r.Context().Done()
		}
	})
	var got []ResyncInfo
	opts := feedOpts()
	opts.OnResync = func(ctx context.Context, info ResyncInfo) { got = append(got, info) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := fs.client(t).Realtime.Subscribe(ctx, "collections.t.*", opts)
	first, second := recv(t, events), recv(t, events)
	if first.EventID != "evt_A" || second.EventID != "evt_A" {
		t.Fatalf("events %+v %+v (resync must not be yielded)", first, second)
	}
	if len(got) != 1 || got[0].Reason != "cursor-unknown" || got[0].Action != "reread-source" {
		t.Fatalf("OnResync = %+v", got)
	}
	ids := fs.ids()
	if len(ids) != 3 || ids[0] != "" || ids[1] != "evt_A" || ids[2] != "" {
		t.Fatalf("Last-Event-ID per connection = %q", ids)
	}
}

func TestFeedClientErrorThrownOnceServerErrorRetried(t *testing.T) {
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"code":"K9O_FORBIDDEN","message":"no"}`)
	})
	events, errs := fs.client(t).Realtime.Subscribe(context.Background(), "t", feedOpts())
	for range events {
	}
	kerr := <-errs
	if kerr == nil || kerr.Code != "K9O_FORBIDDEN" || kerr.Status != 403 {
		t.Fatalf("err = %+v", kerr)
	}
	if n := len(fs.ids()); n != 1 {
		t.Fatalf("4xx made %d connections, want 1", n)
	}

	fs2 := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n < 2 {
			w.WriteHeader(503)
			return
		}
		sendSSE(w, feedFrame("evt_A", "row_inserted", "r1"))
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ = fs2.client(t).Realtime.Subscribe(ctx, "t", feedOpts())
	if ev := recv(t, events); ev.EventID != "evt_A" {
		t.Fatalf("ev = %+v", ev)
	}
	if n := len(fs2.ids()); n != 3 {
		t.Fatalf("connections = %d, want 3 (two 503s then success)", n)
	}
}

func TestFeedCancelStopsReconnecting(t *testing.T) {
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		sendSSE(w, feedFrame(fmt.Sprintf("evt_%d", n), "row_inserted", "r"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	events, errs := fs.client(t).Realtime.Subscribe(ctx, "t", feedOpts())
	recv(t, events)
	recv(t, events)
	cancel()
	deadline := time.After(3 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-events:
		case <-deadline:
			t.Fatal("events did not close after cancel")
		}
	}
	for range errs {
	}
	n := len(fs.ids())
	time.Sleep(100 * time.Millisecond)
	if after := len(fs.ids()); after != n {
		t.Fatalf("reconnected after cancel: %d -> %d", n, after)
	}
}

func TestFeedIdleConnectionIsAbandoned(t *testing.T) {
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 0 {
			sendSSE(w, feedFrame("evt_A", "row_inserted", "r1"))
			<-r.Context().Done() // silent: no bytes, no heartbeat
			return
		}
		sendSSE(w, feedFrame("evt_B", "row_inserted", "r2"))
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := fs.client(t).Realtime.Subscribe(ctx, "t", feedOpts())
	recv(t, events)
	if ev := recv(t, events); ev.EventID != "evt_B" {
		t.Fatalf("ev = %+v", ev)
	}
	if ids := fs.ids(); len(ids) != 2 || ids[1] != "evt_A" {
		t.Fatalf("Last-Event-ID per connection = %q", ids)
	}
}

func TestFeedHeartbeatsKeepConnectionAlive(t *testing.T) {
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 5; i++ { // 5 x 100ms of heartbeats > the 300ms idle limit
			sendSSE(w, ":hb\n\n")
			time.Sleep(100 * time.Millisecond)
		}
		sendSSE(w, feedFrame("evt_A", "row_inserted", "r1"))
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := fs.client(t).Realtime.Subscribe(ctx, "t", feedOpts())
	if ev := recv(t, events); ev.EventID != "evt_A" {
		t.Fatalf("ev = %+v", ev)
	}
	if n := len(fs.ids()); n != 1 {
		t.Fatalf("heartbeats should keep one connection, got %d", n)
	}
}

func TestFeedDedupeBounded(t *testing.T) {
	d := newFeedDedupe()
	for i := 0; i < feedDedupeSize+10; i++ {
		if !d.add(fmt.Sprintf("e%d", i)) {
			t.Fatal("fresh id reported duplicate")
		}
	}
	if len(d.seen) != feedDedupeSize {
		t.Fatalf("set size = %d", len(d.seen))
	}
	if d.add(fmt.Sprintf("e%d", feedDedupeSize+9)) {
		t.Fatal("recent id not remembered")
	}
}

const freshHeadResync = "event: resync\ndata: {\"reason\":\"cursor-evicted\",\"head\":42,\"action\":\"reread-source\"}\n\n"

func assertNoEvent(t *testing.T, ch <-chan RealtimeEvent) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("unexpected event %+v", ev)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFeedOpeningResyncOnFreshConnectionIsInformational(t *testing.T) {
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		sendSSE(w, "retry: 5\n:ok\n\n"+freshHeadResync+feedFrame("evt_A", "row_inserted", "r1"))
		<-r.Context().Done()
	})
	calls := 0
	opts := feedOpts()
	opts.OnResync = func(context.Context, ResyncInfo) { calls++ }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := fs.client(t).Realtime.Subscribe(ctx, "collections.t.*", opts)
	if ev := recv(t, events); ev.EventID != "evt_A" {
		t.Fatalf("got %+v", ev)
	}
	assertNoEvent(t, events)
	if calls != 0 {
		t.Fatalf("OnResync called %d times for the opening resync", calls)
	}
	if ids := fs.ids(); len(ids) != 1 {
		t.Fatalf("reconnected: Last-Event-ID per connection = %q", ids)
	}
}

func TestMetadataSubscribeOpeningResyncIsInformational(t *testing.T) {
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		sendSSE(w, "retry: 5\n:ok\n\n"+freshHeadResync+feedFrame("evt_A", "row_inserted", "r1"))
		<-r.Context().Done()
	})
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := fs.client(t).Metadata.Subscribe(ctx, "collection:t", MetadataSubscribeOptions{
		OnResync:    func(context.Context, ResyncInfo) { calls++ },
		idleTimeout: 300 * time.Millisecond, retryDelay: 5 * time.Millisecond,
	})
	if ev := recv(t, events); ev.EventID != "evt_A" {
		t.Fatalf("got %+v", ev)
	}
	assertNoEvent(t, events)
	if calls != 0 || len(fs.ids()) != 1 {
		t.Fatalf("OnResync calls %d, connections %q", calls, fs.ids())
	}
}

func TestFeedResyncAfterEventIsReal(t *testing.T) {
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 0 {
			sendSSE(w, ":ok\n\n"+feedFrame("evt_A", "row_inserted", "r1")+freshHeadResync)
			return
		}
		sendSSE(w, feedFrame("evt_B", "row_inserted", "r2"))
		<-r.Context().Done()
	})
	var calls int
	opts := feedOpts()
	opts.OnResync = func(context.Context, ResyncInfo) { calls++ }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := fs.client(t).Realtime.Subscribe(ctx, "collections.t.*", opts)
	if a, b := recv(t, events), recv(t, events); a.EventID != "evt_A" || b.EventID != "evt_B" {
		t.Fatalf("got %+v %+v", a, b)
	}
	if calls != 1 {
		t.Fatalf("OnResync calls = %d, want 1", calls)
	}
	if ids := fs.ids(); len(ids) != 2 || ids[1] != "" {
		t.Fatalf("Last-Event-ID per connection = %q", ids)
	}
}

func TestFeedResyncOnConnectionWithLastEventIDIsReal(t *testing.T) {
	fs := newFeedServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 0:
			sendSSE(w, feedFrame("evt_A", "row_inserted", "r1"))
		case 1: // sent Last-Event-ID: the resync is first data but real
			sendSSE(w, freshHeadResync)
		default:
			sendSSE(w, feedFrame("evt_B", "row_inserted", "r2"))
			<-r.Context().Done()
		}
	})
	var calls int
	opts := feedOpts()
	opts.OnResync = func(context.Context, ResyncInfo) { calls++ }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := fs.client(t).Realtime.Subscribe(ctx, "collections.t.*", opts)
	if a, b := recv(t, events), recv(t, events); a.EventID != "evt_A" || b.EventID != "evt_B" {
		t.Fatalf("got %+v %+v", a, b)
	}
	ids := fs.ids()
	if calls != 1 || len(ids) != 3 || ids[1] != "evt_A" || ids[2] != "" {
		t.Fatalf("OnResync calls %d, Last-Event-ID per connection = %q", calls, ids)
	}
}
