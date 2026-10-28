package app

import (
	"context"
	"fmt"
	"sort"
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
	running int
	stopped bool
	workers sync.WaitGroup
	idle    sync.WaitGroup
}

func newReviewQueue(a *App, ctx context.Context) *reviewQueue {
	q := &reviewQueue{app: a, ctx: ctx}
	q.ready = sync.NewCond(&q.mu)
	a.stateMu.Lock()
	for _, req := range a.State.Pending {
		q.items = append(q.items, req)
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
		q.running--
		q.mu.Unlock()
		q.idle.Done()
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
