package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/trigger"
)

// reviewQueue owns the in-memory FIFO. Pending requests are also saved in State
// before acknowledgement, so a service restart replays unfinished work.
type reviewQueue struct {
	app     *App
	ctx     context.Context
	mu      sync.Mutex
	ready   *sync.Cond
	items   []trigger.Request
	queued  map[int64]bool // requests waiting in items or being reviewed
	running int
	stopped bool
	workers sync.WaitGroup
	idle    sync.WaitGroup
}

func newReviewQueue(a *App, ctx context.Context) *reviewQueue {
	q := &reviewQueue{app: a, ctx: ctx, queued: map[int64]bool{}}
	q.ready = sync.NewCond(&q.mu)
	now := time.Now()
	a.stateMu.Lock()
	for _, req := range a.State.Pending {
		if req.RetryAt.After(now) {
			continue // waiting out a temporary GitHub failure; retryDue picks it up
		}
		q.items = append(q.items, req)
		q.queued[req.CommentID] = true
		q.idle.Add(1)
	}
	a.stateMu.Unlock()
	sort.Slice(q.items, func(i, j int) bool { return q.items[i].CommentID < q.items[j].CommentID })
	return q
}
func (q *reviewQueue) start() {
	for i := 0; i < 3; i++ {
		q.workers.Add(1)
		go q.worker()
	}
	go func() { <-q.ctx.Done(); q.mu.Lock(); q.ready.Broadcast(); q.mu.Unlock() }()
}
func (q *reviewQueue) enqueue(req trigger.Request) bool {
	q.app.stateMu.Lock()
	if q.app.State.Done(req.CommentID) || q.app.State.Pending[req.CommentID].CommentID != 0 {
		q.app.stateMu.Unlock()
		return true
	}
	if req.Seen.IsZero() {
		req.Seen = time.Now()
	}
	q.app.State.Pending[req.CommentID] = req
	if err := q.app.State.Save(); err != nil {
		delete(q.app.State.Pending, req.CommentID)
		q.app.stateMu.Unlock()
		q.app.logf("%s#%d: saving queue failed: %v", req.Repo, req.PR, err)
		return false
	}
	q.app.stateMu.Unlock()
	q.mu.Lock()
	position := len(q.items) + q.running + 1
	q.items = append(q.items, req)
	q.queued[req.CommentID] = true
	q.idle.Add(1)
	q.ready.Signal()
	q.mu.Unlock()
	q.app.logf("%s#%d: queued at position %d", req.Repo, req.PR, position)
	// A visible queue receipt is sent once per request, after durable storage.
	if position > 3 {
		noticeCtx, cancel := context.WithTimeout(q.ctx, 30*time.Second)
		defer cancel()
		if err := q.app.Host.Comment(noticeCtx, req.Repo, req.PR, fmt.Sprintf("Otto recebeu a revisão e a colocou na fila (posição %d; até 3 revisões simultâneas).", position)); err != nil {
			q.app.logf("%s#%d: queue notice failed: %v", req.Repo, req.PR, err)
		}
	}
	return true
}
func (q *reviewQueue) worker() {
	defer q.workers.Done()
	for {
		q.mu.Lock()
		for len(q.items) == 0 && !q.stopped && q.ctx.Err() == nil {
			q.ready.Wait()
		}
		if q.stopped || q.ctx.Err() != nil {
			q.mu.Unlock()
			return
		}
		req := q.items[0]
		q.items = q.items[1:]
		q.running++
		q.mu.Unlock()
		q.app.handle(q.ctx, req)
		q.mu.Lock()
		delete(q.queued, req.CommentID)
		q.running--
		q.mu.Unlock()
		q.idle.Done()
	}
}

// retryDue puts back in line the requests whose retry time after a temporary GitHub failure
// has come. They run again silently; nothing is posted until they finish.
func (q *reviewQueue) retryDue(now time.Time) {
	q.app.stateMu.Lock()
	var due []trigger.Request
	for _, req := range q.app.State.Pending {
		if !req.RetryAt.IsZero() && !req.RetryAt.After(now) {
			due = append(due, req)
		}
	}
	q.app.stateMu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].CommentID < due[j].CommentID })
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, req := range due {
		if q.queued[req.CommentID] {
			continue
		}
		q.app.logf("%s#%d: retrying (attempt %d)", req.Repo, req.PR, req.Attempts+1)
		q.items = append(q.items, req)
		q.queued[req.CommentID] = true
		q.idle.Add(1)
		q.ready.Signal()
	}
}

func (q *reviewQueue) wait() {
	done := make(chan struct{})
	go func() { q.idle.Wait(); close(done) }()
	select {
	case <-done:
	case <-q.ctx.Done():
	}
}
func (q *reviewQueue) stop() {
	q.mu.Lock()
	q.stopped = true
	q.ready.Broadcast()
	q.mu.Unlock()
	q.workers.Wait()
}

// QueueReport describes the review requests Otto has seen but not finished: waiting their
// turn, under review, or waiting to retry after a temporary GitHub failure. Finished requests
// (reviewed, or reported as failed on the pull request) are not listed.
func QueueReport(pending map[int64]trigger.Request, now time.Time) string {
	if len(pending) == 0 {
		return "No review requests waiting.\n"
	}
	reqs := make([]trigger.Request, 0, len(pending))
	for _, r := range pending {
		reqs = append(reqs, r)
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].CommentID < reqs[j].CommentID })
	var b strings.Builder
	for _, r := range reqs {
		fmt.Fprintf(&b, "%s#%d  requested by %s", r.Repo, r.PR, r.Author)
		if !r.Seen.IsZero() {
			fmt.Fprintf(&b, " %s ago", now.Sub(r.Seen).Round(time.Minute))
		}
		if r.RetryAt.IsZero() {
			b.WriteString("  · waiting or under review\n")
			continue
		}
		fmt.Fprintf(&b, "  · retry %d at %s", r.Attempts+1, r.RetryAt.Local().Format("15:04"))
		if r.LastError != "" {
			fmt.Fprintf(&b, "\n    last failure: %s", r.LastError)
		}
		b.WriteString("\n")
	}
	return b.String()
}
