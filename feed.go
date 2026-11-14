package k9o

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	feedIdleTimeout   = 45 * time.Second
	feedDefaultRetry  = time.Second
	feedMaxBackoff    = 30 * time.Second
	feedDedupeSize    = 1024
	feedMaxFailShift  = 16
	feedLastEventHdr  = "Last-Event-ID"
	feedResyncEvent   = "resync"
	feedDoneSentinel  = "[DONE]"
	feedScannerMaxBuf = 1024 * 1024
)

// idleReader arms a timer around each blocking Read so that a connection that
// delivers no bytes for the idle limit is cancelled. The timer is stopped
// while the consumer is busy, so a slow consumer never trips it.
type idleReader struct {
	r     io.Reader
	idle  time.Duration
	timer *time.Timer
}

func (i *idleReader) Read(p []byte) (int, error) {
	i.timer.Reset(i.idle)
	n, err := i.r.Read(p)
	i.timer.Stop()
	return n, err
}

// feedDedupe remembers the most recent event ids.
type feedDedupe struct {
	seen map[string]struct{}
	ring []string
	next int
}

func newFeedDedupe() *feedDedupe {
	return &feedDedupe{seen: map[string]struct{}{}, ring: make([]string, 0, feedDedupeSize)}
}

// add reports false when id was already seen.
func (d *feedDedupe) add(id string) bool {
	if _, ok := d.seen[id]; ok {
		return false
	}
	if len(d.ring) < feedDedupeSize {
		d.ring = append(d.ring, id)
	} else {
		delete(d.seen, d.ring[d.next])
		d.ring[d.next] = id
		d.next = (d.next + 1) % feedDedupeSize
	}
	d.seen[id] = struct{}{}
	return true
}

func (d *feedDedupe) clear() { *d = *newFeedDedupe() }

// feedState carries the resume position across connections.
type feedState struct {
	lastID string
	dedupe *feedDedupe
	// positioned is true once the feed has a position of its own (an event was
	// delivered or a resync happened); the initial query is sent only before that.
	positioned bool
	// delivered counts events handed to the consumer by live frames; resyncAt
	// is its value at the last resync.
	delivered int
	resyncAt  int
	resynced  bool
	// pendingCatchUp is true after a resync on a feed with a catchUp hook:
	// the next connection that opens is fresh (no Last-Event-ID) and starts
	// the catch-up.
	pendingCatchUp bool

	// failure is set by the catch-up when it cannot continue; failCh wakes the
	// feed loop, cancelConn ends the connection that is open.
	failMu     sync.Mutex
	failure    *KError
	cancelConn func()
	failCh     chan struct{}
}

// fail records the first failure of a background catch-up and stops the
// connection that is open.
func (st *feedState) fail(kerr *KError) {
	st.failMu.Lock()
	if st.failure == nil {
		st.failure = kerr
	}
	cancel := st.cancelConn
	st.failMu.Unlock()
	select {
	case st.failCh <- struct{}{}:
	default:
	}
	if cancel != nil {
		cancel()
	}
}

func (st *feedState) takeFailure() *KError {
	st.failMu.Lock()
	defer st.failMu.Unlock()
	return st.failure
}

func (st *feedState) setCancel(c func()) {
	st.failMu.Lock()
	st.cancelConn = c
	st.failMu.Unlock()
}

// feedOutcome is how one connection ended.
type feedOutcome struct {
	progressed bool          // at least one frame arrived
	resync     bool          // a resync frame was handled
	fatal      *KError       // stop and report
	done       bool          // stop silently (ctx cancelled or [DONE])
	retry      time.Duration // server's retry hint, 0 if none
}

// feedHooks adapts the feed to one kind of frame.
type feedHooks[T any] struct {
	// decode turns one data frame into an event. name and id are the frame's
	// SSE event name and SSE id. key is the id used to drop duplicates (the
	// frame's own eventId); empty means the frame has none. The position to
	// resume from (Last-Event-ID) is the SSE id, not key, when the frame has
	// one. ok false skips the frame.
	decode func(name, id, payload string) (ev T, key string, ok bool)
	// deliver, when set, hands a decoded, not yet seen event to the consumer
	// through emit (or holds it back). It returns false to end the feed.
	deliver func(ev T, emit func(T) bool) bool
	// catchUp, when set, changes what a resync means. The feed drops its
	// position and reconnects fresh (no Last-Event-ID); once that connection
	// is open, catchUp is called (it must not block) so the hook can read what
	// it missed in the background while the frames of the open connection are
	// held back by deliver. fail reports an error that ends the feed.
	catchUp func(ctx context.Context, emit func(T) bool, fail func(*KError))
}

func realtimeHooks() feedHooks[RealtimeEvent] {
	return feedHooks[RealtimeEvent]{decode: func(_, id, payload string) (RealtimeEvent, string, bool) {
		var ev RealtimeEvent
		if json.Unmarshal([]byte(payload), &ev) != nil || ev.EventType == "" {
			return ev, "", false
		}
		if ev.EventID == "" {
			ev.EventID = id
		}
		return ev, ev.EventID, true
	}}
}

// streamFeed runs the change-feed subscription: reconnect with Last-Event-ID,
// de-duplicate by eventId, hand resync frames to OnResync, and drop dead
// connections after the idle limit.
func streamFeed(ctx context.Context, cfg *httpClientConfig, init *requestInit, opts SubscribeOptions) (<-chan RealtimeEvent, <-chan *KError) {
	return streamFeedOf(ctx, cfg, init, opts, realtimeHooks())
}

// streamFeedOf is streamFeed for any frame type.
func streamFeedOf[T any](ctx context.Context, cfg *httpClientConfig, init *requestInit, opts SubscribeOptions, hooks feedHooks[T]) (<-chan T, <-chan *KError) {
	events := make(chan T)
	errs := make(chan *KError, 1)

	idle := opts.idleTimeout
	if idle <= 0 {
		idle = feedIdleTimeout
	}
	baseDelay := opts.retryDelay
	if baseDelay <= 0 {
		baseDelay = feedDefaultRetry
	}

	go func() {
		defer close(events)
		defer close(errs)

		st := &feedState{dedupe: newFeedDedupe(), failCh: make(chan struct{}, 1)}
		serverRetry := time.Duration(0)
		fails := 0

		for ctx.Err() == nil {
			out := feedConnect(ctx, cfg, init, opts, idle, st, events, hooks)
			if f := st.takeFailure(); f != nil {
				errs <- f
				return
			}
			if out.retry > 0 {
				serverRetry = out.retry
			}
			if out.fatal != nil {
				errs <- out.fatal
				return
			}
			if out.done || ctx.Err() != nil {
				return
			}
			if out.resync {
				// A resync that follows a resync with no event in between
				// backs off like a failed connection, so it cannot spin.
				repeat := st.resynced && st.delivered == st.resyncAt
				st.resynced, st.resyncAt = true, st.delivered
				st.positioned = true
				st.lastID = ""
				if hooks.catchUp != nil {
					st.pendingCatchUp = true
				} else {
					st.dedupe.clear()
				}
				if repeat {
					fails++
				} else {
					fails = 0
				}
			} else if out.progressed {
				fails = 0
			} else {
				fails++
			}

			delay := baseDelay
			if serverRetry > 0 {
				delay = serverRetry
			}
			if fails > 1 {
				shift := fails - 1
				if shift > feedMaxFailShift {
					shift = feedMaxFailShift
				}
				delay <<= uint(shift)
			}
			if delay > feedMaxBackoff {
				delay = feedMaxBackoff
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			case <-st.failCh:
				timer.Stop()
			}
		}
	}()

	return events, errs
}

// feedConnect opens one connection and reads it until it ends.
func feedConnect[T any](ctx context.Context, cfg *httpClientConfig, init *requestInit, opts SubscribeOptions, idle time.Duration, st *feedState, events chan<- T, hooks feedHooks[T]) feedOutcome {
	var out feedOutcome

	urlStr := strings.TrimRight(cfg.resolveOrigin(ctx), "/") + init.Path
	query := map[string]string{}
	for k, v := range init.Query {
		query[k] = v
	}
	if !st.positioned {
		for k, v := range opts.initialQuery {
			query[k] = v
		}
	}
	if len(query) > 0 {
		params := []string{}
		for k, v := range query {
			params = append(params, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
		urlStr = urlStr + "?" + strings.Join(params, "&")
	}

	headers := cfg.buildHeaders(init)
	headers.Set("Accept", "text/event-stream")
	if st.lastID != "" {
		headers.Set(feedLastEventHdr, st.lastID)
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	st.setCancel(cancel)
	defer st.setCancel(nil)
	emit := func(v T) bool {
		select {
		case events <- v:
			return true
		case <-ctx.Done():
			return false
		}
	}
	req, err := http.NewRequestWithContext(connCtx, init.Method, urlStr, nil)
	if err != nil {
		out.fatal = &KError{Code: "K9O_INTERNAL_ERROR", Message: fmt.Sprintf("failed to build request: %v", err)}
		return out
	}
	req.Header = headers

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	// The idle timer also covers waiting for the response headers.
	timer := time.AfterFunc(idle, cancel)
	defer timer.Stop()
	resp, err := httpClient.Do(req)
	if err != nil {
		return out // transport error: the caller retries unless ctx is done
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			var wire KErrorWire
			if json.Unmarshal(respBytes, &wire) == nil && wire.Code != "" && wire.Message != "" {
				out.fatal = withRequestID(kerrorFromWire(&wire, resp.StatusCode), resp.Header)
				return out
			}
			out.fatal = withRequestID(&KError{
				Code:    "K9O_INTERNAL_ERROR",
				Message: fmt.Sprintf("upstream returned %d opening stream", resp.StatusCode),
				Context: map[string]any{"url": urlStr, "status": resp.StatusCode},
				Status:  resp.StatusCode,
			}, resp.Header)
			return out
		}
		return out // 5xx and other statuses: retry
	}

	ir := &idleReader{r: resp.Body, idle: idle, timer: timer}
	scanner := bufio.NewScanner(ir)
	scanner.Buffer(make([]byte, 0, 64*1024), feedScannerMaxBuf)

	// fresh: this connection sent no position, neither a Last-Event-ID nor a
	// fromSequence query (the gateway treats both as a cursor).
	_, sentFromSequence := query["fromSequence"]
	fresh := st.lastID == "" && !sentFromSequence
	if st.pendingCatchUp && hooks.catchUp != nil {
		st.pendingCatchUp = false
		hooks.catchUp(ctx, emit, st.fail)
	}

	var dataLines []string
	var evName, evID string
	reset := func() { dataLines = dataLines[:0]; evName, evID = "", "" }

	// flush handles one complete frame; it returns false to end the connection.
	flush := func() bool {
		defer reset()
		if len(dataLines) == 0 {
			return true
		}
		wasProgressed := out.progressed
		out.progressed = true
		payload := strings.Join(dataLines, "\n")
		if payload == feedDoneSentinel {
			out.done = true
			return false
		}
		if evName == feedResyncEvent {
			if fresh && !wasProgressed {
				// The first data on a fresh connection (no position
				// sent) is the gateway's informational head marker: the
				// stream stays open and live frames follow. Nothing to
				// reconnect to, nothing to report, no progress either.
				out.progressed = false
				return true
			}
			var info ResyncInfo
			_ = json.Unmarshal([]byte(payload), &info)
			if opts.OnResync != nil {
				opts.OnResync(ctx, info)
			}
			out.resync = true
			return false
		}
		ev, key, ok := hooks.decode(evName, evID, payload)
		if !ok {
			return true
		}
		if key != "" && !st.dedupe.add(key) {
			return true
		}
		if hooks.deliver != nil {
			if !hooks.deliver(ev, emit) {
				out.done = true
				return false
			}
		} else if !emit(ev) {
			out.done = true
			return false
		}
		st.delivered++
		switch {
		case evID != "":
			st.lastID = evID
			st.positioned = true
		case key != "":
			st.lastID = key
			st.positioned = true
		}
		return true
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "":
			if !flush() {
				return out
			}
		case strings.HasPrefix(line, ":"):
			// comment or heartbeat
		case strings.HasPrefix(line, "retry:"):
			if ms, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "retry:"))); err == nil && ms > 0 {
				out.retry = time.Duration(ms) * time.Millisecond
			}
		case strings.HasPrefix(line, "event:"):
			evName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "id:"):
			evID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	// A frame cut off by the end of the stream is incomplete: drop it.
	if ctx.Err() != nil {
		out.done = true
	}
	return out
}
