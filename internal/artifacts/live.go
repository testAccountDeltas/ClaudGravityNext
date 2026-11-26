package artifacts

import (
	"encoding/json"
	"sync"
	"time"
)

const (
	liveSubprotocol = "frame-live.v1"
	liveTokenTTL    = 2 * time.Hour
	wsSendQueueCap  = 64
	sseSendQueueCap = 32
	wsReadTimeout   = 120 * time.Second
	wsWriteTimeout  = 15 * time.Second
	maxActivitySec  = 300
)

type sseEvent struct {
	Name string
	Data string
}

type wsClient struct {
	send chan string
	done chan struct{}
}

func (c *wsClient) trySend(msg string) {
	select {
	case c.send <- msg:
	case <-c.done:
	default:
	}
}

type sseClient struct {
	send chan sseEvent
	done chan struct{}
}

func (c *sseClient) trySend(ev sseEvent) {
	select {
	case c.send <- ev:
	case <-c.done:
	default:
	}
}

type activityLease struct {
	until time.Time
	timer *time.Timer
}

// LiveHub tracks active CLI frame-live.v1 WebSockets, browser SSE viewers,
// and short-lived agent activity indicators per artifact slug.
type LiveHub struct {
	mu         sync.Mutex
	wsClients  map[string]map[*wsClient]struct{}
	sseClients map[string]map[*sseClient]struct{}
	activities map[string]map[*wsClient]*activityLease
}

// NewLiveHub creates a LiveHub.
func NewLiveHub() *LiveHub {
	return &LiveHub{
		wsClients:  make(map[string]map[*wsClient]struct{}),
		sseClients: make(map[string]map[*sseClient]struct{}),
		activities: make(map[string]map[*wsClient]*activityLease),
	}
}

func (l *LiveHub) registerWS(slug string, c *wsClient) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.wsClients[slug]
	if m == nil {
		m = make(map[*wsClient]struct{})
		l.wsClients[slug] = m
	}
	m[c] = struct{}{}
}

func (l *LiveHub) unregisterWS(slug string, c *wsClient) {
	var changed, nowActive bool
	l.mu.Lock()
	if m := l.wsClients[slug]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(l.wsClients, slug)
		}
	}
	changed, nowActive = l.clearClientActivityLocked(slug, c)
	l.mu.Unlock()
	if changed {
		l.broadcastActivity(slug, nowActive)
	}
}

func (l *LiveHub) registerSSE(slug string, c *sseClient) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.sseClients[slug]
	if m == nil {
		m = make(map[*sseClient]struct{})
		l.sseClients[slug] = m
	}
	m[c] = struct{}{}
}

func (l *LiveHub) unregisterSSE(slug string, c *sseClient) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if m := l.sseClients[slug]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(l.sseClients, slug)
		}
	}
}

// IsActive reports whether any connected CLI session has an active turn lease
// for slug.
func (l *LiveHub) IsActive(slug string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.isActiveLocked(slug, time.Now())
}

func (l *LiveHub) isActiveLocked(slug string, now time.Time) bool {
	for _, lease := range l.activities[slug] {
		if lease.until.After(now) {
			return true
		}
	}
	return false
}

func (l *LiveHub) clearClientActivityLocked(slug string, c *wsClient) (changed, nowActive bool) {
	m := l.activities[slug]
	if m == nil {
		return false, false
	}
	wasActive := l.isActiveLocked(slug, time.Now())
	if lease, ok := m[c]; ok {
		if lease.timer != nil {
			lease.timer.Stop()
		}
		delete(m, c)
	}
	if len(m) == 0 {
		delete(l.activities, slug)
	}
	nowActive = l.isActiveLocked(slug, time.Now())
	return wasActive != nowActive, nowActive
}

func (l *LiveHub) setActivity(slug string, c *wsClient, ttl time.Duration) {
	now := time.Now()
	l.mu.Lock()
	wasActive := l.isActiveLocked(slug, now)
	if ttl <= 0 {
		if m := l.activities[slug]; m != nil {
			if lease, ok := m[c]; ok {
				if lease.timer != nil {
					lease.timer.Stop()
				}
				delete(m, c)
			}
			if len(m) == 0 {
				delete(l.activities, slug)
			}
		}
	} else {
		m := l.activities[slug]
		if m == nil {
			m = make(map[*wsClient]*activityLease)
			l.activities[slug] = m
		}
		if prev, ok := m[c]; ok && prev.timer != nil {
			prev.timer.Stop()
		}
		until := now.Add(ttl)
		lease := &activityLease{until: until}
		lease.timer = time.AfterFunc(ttl+time.Millisecond, func() {
			l.expireActivity(slug, c, lease)
		})
		m[c] = lease
	}
	nowActive := l.isActiveLocked(slug, now)
	l.mu.Unlock()
	if wasActive != nowActive {
		l.broadcastActivity(slug, nowActive)
	}
}

func (l *LiveHub) expireActivity(slug string, c *wsClient, target *activityLease) {
	l.mu.Lock()
	now := time.Now()
	wasActive := l.isActiveLocked(slug, now)
	if m := l.activities[slug]; m != nil && m[c] == target {
		delete(m, c)
		if len(m) == 0 {
			delete(l.activities, slug)
		}
	}
	nowActive := l.isActiveLocked(slug, now)
	l.mu.Unlock()
	if wasActive && !nowActive {
		l.broadcastActivity(slug, false)
	}
}

func (l *LiveHub) broadcastActivity(slug string, active bool) {
	b, _ := json.Marshal(map[string]any{"active": active})
	l.sendSSE(slug, sseEvent{Name: "activity", Data: string(b)})
}

// BroadcastVersion notifies connected CLI watchers and browser viewers that
// an artifact was published or updated.
func (l *LiveHub) BroadcastVersion(a Artifact) {
	if l == nil {
		return
	}
	wsPayload, _ := json.Marshal(map[string]any{"ver": a.VersionString()})
	l.sendWS(a.Slug, string(wsPayload))

	title := a.Title
	if title == "" {
		title = a.Slug
	}
	ssePayload, _ := json.Marshal(map[string]any{
		"slug":         a.Slug,
		"version":      a.VersionString(),
		"title":        title,
		"updated_at":   a.UpdatedAt.UTC().Format(time.RFC3339),
		"shimmed_html": injectArtifactShim(a.Content),
		"raw_html":     a.Content,
	})
	l.sendSSE(a.Slug, sseEvent{Name: "version", Data: string(ssePayload)})
}

// BroadcastTitle notifies browser viewers when an artifact title changes.
func (l *LiveHub) BroadcastTitle(slug, title string) {
	if l == nil {
		return
	}
	b, _ := json.Marshal(map[string]any{"slug": slug, "title": title})
	l.sendSSE(slug, sseEvent{Name: "retitle", Data: string(b)})
}

// BroadcastDeleted notifies browser viewers that the artifact was deleted.
func (l *LiveHub) BroadcastDeleted(slug string) {
	if l == nil {
		return
	}
	b, _ := json.Marshal(map[string]any{"slug": slug})
	l.sendSSE(slug, sseEvent{Name: "deleted", Data: string(b)})
}

// BroadcastComment notifies browser SSE listeners of updated comment threads
// and wakes connected CLI watchers when wakeClaude is true.
func (l *LiveHub) BroadcastComment(slug string, threads []WireThread, wakeClaude bool) {
	if l == nil {
		return
	}
	if wakeClaude {
		l.sendWS(slug, `{"kind":"comment"}`)
	}
	if threads == nil {
		threads = []WireThread{}
	}
	b, _ := json.Marshal(map[string]any{"threads": threads})
	l.sendSSE(slug, sseEvent{Name: "comments", Data: string(b)})
}

func (l *LiveHub) sendWS(slug, msg string) {
	l.mu.Lock()
	var targets []*wsClient
	for c := range l.wsClients[slug] {
		targets = append(targets, c)
	}
	l.mu.Unlock()
	for _, c := range targets {
		c.trySend(msg)
	}
}

func (l *LiveHub) sendSSE(slug string, ev sseEvent) {
	l.mu.Lock()
	var targets []*sseClient
	for c := range l.sseClients[slug] {
		targets = append(targets, c)
	}
	l.mu.Unlock()
	for _, c := range targets {
		c.trySend(ev)
	}
}
