package oban

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestChainOrdering(t *testing.T) {
	var order []string
	record := func(tag string) Middleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, job *Job) error {
				order = append(order, tag+"-pre")
				err := next(ctx, job)
				order = append(order, tag+"-post")
				return err
			}
		}
	}
	final := func(_ context.Context, _ *Job) error {
		order = append(order, "worker")
		return nil
	}
	h := chain(final, []Middleware{record("a"), record("b"), record("c")})
	if err := h(context.Background(), &Job{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a-pre", "b-pre", "c-pre", "worker", "c-post", "b-post", "a-post"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestMiddlewareOrderingThroughEngine(t *testing.T) {
	var mu sync.Mutex
	var order []string
	add := func(tag string) {
		mu.Lock()
		order = append(order, tag)
		mu.Unlock()
	}
	record := func(tag string) Middleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, job *Job) error {
				add(tag + "-pre")
				err := next(ctx, job)
				add(tag + "-post")
				return err
			}
		}
	}

	// A sentinel middleware sits outermost; its post runs last, after every
	// other post, so closing done there guarantees order is fully populated.
	done := make(chan struct{})
	sentinel := func(next Handler) Handler {
		return func(ctx context.Context, job *Job) error {
			err := next(ctx, job)
			close(done)
			return err
		}
	}

	eng, err := New(Config{
		Store:        NewInMemoryStore(),
		Queues:       map[string]int{DefaultQueue: 1},
		Clock:        newFakeClock(baseTime),
		PollInterval: time.Millisecond,
		Logger:       discardLogger(),
		Middleware:   []Middleware{sentinel, record("outer"), record("inner")},
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.RegisterFunc("w", func(_ context.Context, _ *Job) error {
		add("worker")
		return nil
	})

	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}

	job := mustJob(t, "w", nil)
	if _, _, err := eng.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	<-done
	_ = eng.Stop(context.Background())

	want := []string{"outer-pre", "inner-pre", "worker", "inner-post", "outer-post"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestTelemetryHooks(t *testing.T) {
	clock := newFakeClock(baseTime)
	var (
		mu        sync.Mutex
		starts    int
		completes int
		failures  int
	)
	tel := &Telemetry{
		OnStart: func(_ context.Context, _ *Job) {
			mu.Lock()
			starts++
			mu.Unlock()
		},
		OnComplete: func(_ context.Context, _ *Job, _ time.Duration) {
			mu.Lock()
			completes++
			mu.Unlock()
		},
		OnError: func(_ context.Context, _ *Job, _ error, _ time.Duration) {
			mu.Lock()
			failures++
			mu.Unlock()
		},
	}

	errCh := make(chan struct{}, 1)
	eng, err := New(Config{
		Store:        NewInMemoryStore(),
		Queues:       map[string]int{DefaultQueue: 2},
		Clock:        clock,
		PollInterval: time.Millisecond,
		Logger:       discardLogger(),
		Telemetry:    tel,
		Backoff:      &ExponentialBackoff{Base: time.Hour},
		ErrorHandler: func(_ *Job, _ error) { errCh <- struct{}{} },
	})
	if err != nil {
		t.Fatal(err)
	}

	okCh := make(chan struct{}, 1)
	eng.RegisterFunc("ok", func(_ context.Context, _ *Job) error { okCh <- struct{}{}; return nil })
	eng.RegisterFunc("bad", func(_ context.Context, _ *Job) error { return errors.New("no") })

	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := eng.Enqueue(ctx, mustJob(t, "ok", nil)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := eng.Enqueue(ctx, mustJob(t, "bad", nil, WithMaxAttempts(5))); err != nil {
		t.Fatal(err)
	}
	<-okCh
	<-errCh
	_ = eng.Stop(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if starts < 2 {
		t.Errorf("OnStart called %d times, want >= 2", starts)
	}
	if completes != 1 {
		t.Errorf("OnComplete called %d times, want 1", completes)
	}
	if failures != 1 {
		t.Errorf("OnError called %d times, want 1", failures)
	}
}
