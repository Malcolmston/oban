package oban

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// eventsStepClock is a Clock that advances by a fixed step on every call to
// Now, giving Middleware a deterministic non-zero attempt duration.
type eventsStepClock struct {
	now  time.Time
	step time.Duration
}

func (c *eventsStepClock) Now() time.Time {
	t := c.now
	c.now = c.now.Add(c.step)
	return t
}

func eventsSampleJob() *Job {
	return &Job{
		ID:          7,
		Queue:       "mailers",
		Worker:      "SendEmail",
		Attempt:     2,
		MaxAttempts: 20,
	}
}

func TestRecorderAttachFanOut(t *testing.T) {
	r := NewRecorder()

	var mu sync.Mutex
	var a, b []EventName
	detachA := r.Attach(func(ev Event) {
		mu.Lock()
		a = append(a, ev.Name)
		mu.Unlock()
	})
	r.Attach(func(ev Event) {
		mu.Lock()
		b = append(b, ev.Name)
		mu.Unlock()
	})

	r.Emit(Event{Name: EngineInsert})
	detachA()
	r.Emit(Event{Name: QueuePause})

	if got, want := len(a), 1; got != want {
		t.Fatalf("detached handler got %d events, want %d", got, want)
	}
	if got, want := len(b), 2; got != want {
		t.Fatalf("live handler got %d events, want %d", got, want)
	}
	if a[0] != EngineInsert || b[0] != EngineInsert || b[1] != QueuePause {
		t.Fatalf("unexpected event order: a=%v b=%v", a, b)
	}
}

func TestRecorderDetachIdempotent(t *testing.T) {
	r := NewRecorder()
	var n int
	detach := r.Attach(func(Event) { n++ })
	detach()
	detach() // must not panic

	r.Emit(Event{Name: JobStart})
	if n != 0 {
		t.Fatalf("handler ran %d times after detach, want 0", n)
	}
}

func TestRecorderNilHandler(t *testing.T) {
	r := NewRecorder()
	detach := r.Attach(nil)
	detach() // no-op, must not panic
	r.Emit(Event{Name: JobStop})
	if got := r.Counts()[JobStop]; got != 1 {
		t.Fatalf("Counts[JobStop] = %d, want 1", got)
	}
}

func TestRecorderCounts(t *testing.T) {
	r := NewRecorder()
	emits := []EventName{JobStart, JobStart, JobStop, PluginPrune}
	for _, name := range emits {
		r.Emit(Event{Name: name})
	}

	counts := r.Counts()
	want := map[EventName]int{JobStart: 2, JobStop: 1, PluginPrune: 1}
	for name, wantN := range want {
		if counts[name] != wantN {
			t.Errorf("Counts[%s] = %d, want %d", name, counts[name], wantN)
		}
	}

	// Snapshot must be a copy: mutating it must not affect the Recorder.
	counts[JobStart] = 999
	if r.Counts()[JobStart] != 2 {
		t.Fatalf("Counts snapshot is not a copy")
	}
}

func TestRecorderMiddleware(t *testing.T) {
	sentinel := errors.New("boom")
	tests := []struct {
		name       string
		handlerErr error
		wantSecond EventName
		wantErr    error
	}{
		{"success", nil, JobStop, nil},
		{"failure", sentinel, JobException, sentinel},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRecorder()
			var got []Event
			r.Attach(func(ev Event) { got = append(got, ev) })

			clock := &eventsStepClock{now: baseTime, step: 250 * time.Millisecond}
			mw := r.Middleware(clock)
			h := mw(func(ctx context.Context, job *Job) error { return tc.handlerErr })

			job := eventsSampleJob()
			err := h(context.Background(), job)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("handler err = %v, want %v", err, tc.wantErr)
			}

			if len(got) != 2 {
				t.Fatalf("emitted %d events, want 2", len(got))
			}
			if got[0].Name != JobStart {
				t.Errorf("first event = %s, want %s", got[0].Name, JobStart)
			}
			if got[1].Name != tc.wantSecond {
				t.Errorf("second event = %s, want %s", got[1].Name, tc.wantSecond)
			}

			// Fields populated from the job.
			start := got[0]
			if start.Queue != job.Queue || start.Worker != job.Worker ||
				start.Attempt != job.Attempt || start.MaxAttempts != job.MaxAttempts {
				t.Errorf("start event fields not populated from job: %+v", start)
			}
			if start.Job != job {
				t.Errorf("start event Job = %p, want %p", start.Job, job)
			}

			// Duration measured across the two clock reads (one step).
			stop := got[1]
			if stop.Duration != 250*time.Millisecond {
				t.Errorf("duration = %v, want %v", stop.Duration, 250*time.Millisecond)
			}
			if !errors.Is(stop.Error, tc.wantErr) {
				t.Errorf("stop event Error = %v, want %v", stop.Error, tc.wantErr)
			}
		})
	}
}

func TestRecorderMiddlewareNilClock(t *testing.T) {
	r := NewRecorder()
	var n int
	r.Attach(func(Event) { n++ })
	h := r.Middleware(nil)(func(ctx context.Context, job *Job) error { return nil })
	if err := h(context.Background(), eventsSampleJob()); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != 2 {
		t.Fatalf("emitted %d events, want 2", n)
	}
}

func TestRecorderAsTelemetry(t *testing.T) {
	sentinel := errors.New("kaboom")
	r := NewRecorder()
	var got []Event
	r.Attach(func(ev Event) { got = append(got, ev) })

	clock := newFakeClock(baseTime)
	tel := r.AsTelemetry(clock)

	job := eventsSampleJob()
	ctx := context.Background()
	tel.OnStart(ctx, job)
	tel.OnComplete(ctx, job, 5*time.Second)
	tel.OnError(ctx, job, sentinel, 2*time.Second)

	wantNames := []EventName{JobStart, JobStop, JobException}
	if len(got) != len(wantNames) {
		t.Fatalf("emitted %d events, want %d", len(got), len(wantNames))
	}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Errorf("event[%d] = %s, want %s", i, got[i].Name, want)
		}
		if got[i].At != baseTime {
			t.Errorf("event[%d].At = %v, want %v", i, got[i].At, baseTime)
		}
	}
	if got[1].Duration != 5*time.Second {
		t.Errorf("complete duration = %v, want 5s", got[1].Duration)
	}
	if !errors.Is(got[2].Error, sentinel) {
		t.Errorf("error event Error = %v, want %v", got[2].Error, sentinel)
	}
	if got[2].Duration != 2*time.Second {
		t.Errorf("error duration = %v, want 2s", got[2].Duration)
	}
}

func TestRecorderAsTelemetryPluggable(t *testing.T) {
	// A Recorder's telemetry must satisfy the Config.Telemetry shape and adapt
	// into a Middleware via the existing Telemetry.Middleware helper.
	r := NewRecorder()
	var cfg Config
	cfg.Telemetry = r.AsTelemetry(newFakeClock(baseTime))

	var n int
	r.Attach(func(Event) { n++ })

	mw := cfg.Telemetry.Middleware(newFakeClock(baseTime))
	h := mw(func(ctx context.Context, job *Job) error { return nil })
	if err := h(context.Background(), eventsSampleJob()); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Telemetry.Middleware calls OnStart then OnComplete -> two events.
	if n != 2 {
		t.Fatalf("emitted %d events through Telemetry.Middleware, want 2", n)
	}
}

func TestEventsForJobNil(t *testing.T) {
	ev := eventsForJob(EngineInsert, baseTime, nil, 0, nil)
	if ev.Queue != "" || ev.Worker != "" || ev.Attempt != 0 || ev.MaxAttempts != 0 {
		t.Fatalf("nil job should leave job fields zero: %+v", ev)
	}
	if ev.Name != EngineInsert || ev.At != baseTime {
		t.Fatalf("unexpected name/time: %+v", ev)
	}
}
