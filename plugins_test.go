package oban

import (
	"context"
	"errors"
	"testing"
	"time"
)

// pluginsFakePrunable records DeleteFinishedBefore calls for assertion.
type pluginsFakePrunable struct {
	calls []pluginsPruneCall
	ret   int64
	err   error
}

type pluginsPruneCall struct {
	states []State
	cutoff time.Time
	limit  int
}

func (f *pluginsFakePrunable) DeleteFinishedBefore(_ context.Context, states []State, cutoff time.Time, limit int) (int64, error) {
	f.calls = append(f.calls, pluginsPruneCall{states: states, cutoff: cutoff, limit: limit})
	return f.ret, f.err
}

// pluginsFakeRescuable records RescueExecuting calls for assertion.
type pluginsFakeRescuable struct {
	calls              []pluginsRescueCall
	rescued, discarded int64
	err                error
}

type pluginsRescueCall struct {
	olderThan time.Time
	now       time.Time
}

func (f *pluginsFakeRescuable) RescueExecuting(_ context.Context, olderThan, now time.Time) (int64, int64, error) {
	f.calls = append(f.calls, pluginsRescueCall{olderThan: olderThan, now: now})
	return f.rescued, f.discarded, f.err
}

// pluginsFakeEnqueuer records enqueued jobs and can inject an error.
type pluginsFakeEnqueuer struct {
	workers []string
	err     error
}

func (f *pluginsFakeEnqueuer) Enqueue(_ context.Context, job *Job) (*Job, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	f.workers = append(f.workers, job.Worker)
	return job, true, nil
}

func pluginsEqualStates(a, b []State) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPrunerPrune(t *testing.T) {
	tests := []struct {
		name       string
		cfg        PrunerConfig
		wantStates []State
		wantLimit  int
	}{
		{
			name:       "defaults",
			cfg:        PrunerConfig{MaxAge: time.Hour},
			wantStates: []State{StateCompleted, StateDiscarded, StateCancelled},
			wantLimit:  pluginsDefaultPruneLimit,
		},
		{
			name:       "explicit states and limit",
			cfg:        PrunerConfig{MaxAge: 2 * time.Hour, States: []State{StateCompleted}, Limit: 5},
			wantStates: []State{StateCompleted},
			wantLimit:  5,
		},
		{
			name:       "negative limit falls back to default",
			cfg:        PrunerConfig{MaxAge: 30 * time.Minute, Limit: -1},
			wantStates: []State{StateCompleted, StateDiscarded, StateCancelled},
			wantLimit:  pluginsDefaultPruneLimit,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &pluginsFakePrunable{}
			clock := newFakeClock(baseTime)
			p := NewPruner(store, clock, tt.cfg)
			p.prune(context.Background())

			if len(store.calls) != 1 {
				t.Fatalf("got %d calls, want 1", len(store.calls))
			}
			call := store.calls[0]
			if !pluginsEqualStates(call.states, tt.wantStates) {
				t.Errorf("states = %v, want %v", call.states, tt.wantStates)
			}
			if call.limit != tt.wantLimit {
				t.Errorf("limit = %d, want %d", call.limit, tt.wantLimit)
			}
			wantCutoff := baseTime.Add(-tt.cfg.MaxAge)
			if !call.cutoff.Equal(wantCutoff) {
				t.Errorf("cutoff = %v, want %v", call.cutoff, wantCutoff)
			}
		})
	}
}

func TestPrunerIntervalDefault(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero", 0, pluginsDefaultPruneInterval},
		{"negative", -time.Second, pluginsDefaultPruneInterval},
		{"explicit", 5 * time.Second, 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPruner(&pluginsFakePrunable{}, newFakeClock(baseTime), PrunerConfig{Interval: tt.in})
			if p.interval != tt.want {
				t.Errorf("interval = %v, want %v", p.interval, tt.want)
			}
		})
	}
}

func TestLifelineRescue(t *testing.T) {
	tests := []struct {
		name        string
		rescueAfter time.Duration
	}{
		{"one minute", time.Minute},
		{"five minutes", 5 * time.Minute},
		{"zero", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &pluginsFakeRescuable{rescued: 2, discarded: 1}
			clock := newFakeClock(baseTime)
			l := NewLifeline(store, clock, LifelineConfig{RescueAfter: tt.rescueAfter})
			l.rescue(context.Background())

			if len(store.calls) != 1 {
				t.Fatalf("got %d calls, want 1", len(store.calls))
			}
			call := store.calls[0]
			if !call.now.Equal(baseTime) {
				t.Errorf("now = %v, want %v", call.now, baseTime)
			}
			wantOlder := baseTime.Add(-tt.rescueAfter)
			if !call.olderThan.Equal(wantOlder) {
				t.Errorf("olderThan = %v, want %v", call.olderThan, wantOlder)
			}
		})
	}
}

func TestLifelineIntervalDefault(t *testing.T) {
	l := NewLifeline(&pluginsFakeRescuable{}, newFakeClock(baseTime), LifelineConfig{})
	if l.interval != pluginsDefaultLifelineInterval {
		t.Errorf("interval = %v, want %v", l.interval, pluginsDefaultLifelineInterval)
	}
}

func TestCronPluginFireDue(t *testing.T) {
	tests := []struct {
		name    string
		entries []Periodic
		// steps drives the clock: at each step the clock is Set to baseTime+offset
		// and fireDue is invoked once.
		steps []time.Duration
		want  []string
	}{
		{
			name:    "single every-minute schedule fires once per due minute",
			entries: []Periodic{{Schedule: MustParseCron("* * * * *"), Worker: "beat"}},
			steps:   []time.Duration{0, time.Minute, 2 * time.Minute},
			want:    []string{"beat", "beat"}, // baseTime is 12:30:00; first due at 12:31
		},
		{
			name: "two schedules at different cadences",
			entries: []Periodic{
				{Schedule: MustParseCron("* * * * *"), Worker: "every-minute"},
				{Schedule: MustParseCron("*/2 * * * *"), Worker: "every-two"},
			},
			// 12:31 -> every-minute; 12:32 -> every-minute + every-two.
			steps: []time.Duration{time.Minute, 2 * time.Minute},
			want:  []string{"every-minute", "every-minute", "every-two"},
		},
		{
			name:    "no fire before first due time",
			entries: []Periodic{{Schedule: MustParseCron("* * * * *"), Worker: "beat"}},
			steps:   []time.Duration{0, 30 * time.Second},
			want:    nil,
		},
		{
			name:    "far-future schedule never fires within the window",
			entries: []Periodic{{Schedule: MustParseCron("0 0 1 1 *"), Worker: "never"}},
			steps:   []time.Duration{time.Minute, time.Hour, 24 * time.Hour},
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enq := &pluginsFakeEnqueuer{}
			clock := newFakeClock(baseTime)
			c := NewCronPlugin(enq, clock, tt.entries, time.Second)
			c.buildHeap() // seed heap at baseTime, as the loop goroutine would
			for _, off := range tt.steps {
				clock.Set(baseTime.Add(off))
				c.fireDue(context.Background())
			}
			// Compare as a multiset: when several schedules fire in the same
			// tick, the heap order among equal fire times is unspecified.
			got := map[string]int{}
			for _, w := range enq.workers {
				got[w]++
			}
			want := map[string]int{}
			for _, w := range tt.want {
				want[w]++
			}
			if len(enq.workers) != len(tt.want) {
				t.Fatalf("enqueued %v (%d), want %v (%d)", enq.workers, len(enq.workers), tt.want, len(tt.want))
			}
			for w, n := range want {
				if got[w] != n {
					t.Errorf("worker %q enqueued %d times, want %d", w, got[w], n)
				}
			}
		})
	}
}

func TestCronPluginIntervalDefault(t *testing.T) {
	c := NewCronPlugin(&pluginsFakeEnqueuer{}, newFakeClock(baseTime), nil, 0)
	if c.interval != pluginsDefaultCronInterval {
		t.Errorf("interval = %v, want %v", c.interval, pluginsDefaultCronInterval)
	}
}

func TestPluginNames(t *testing.T) {
	clock := newFakeClock(baseTime)
	plugins := []Plugin{
		NewPruner(&pluginsFakePrunable{}, clock, PrunerConfig{}),
		NewLifeline(&pluginsFakeRescuable{}, clock, LifelineConfig{}),
		NewCronPlugin(&pluginsFakeEnqueuer{}, clock, nil, time.Second),
	}
	want := []string{"pruner", "lifeline", "cron"}
	for i, p := range plugins {
		if p.Name() != want[i] {
			t.Errorf("plugin[%d].Name() = %q, want %q", i, p.Name(), want[i])
		}
	}
}

func TestSupervisorStartStop(t *testing.T) {
	// A pruner with a tiny interval should run and stop cleanly.
	store := &pluginsFakePrunable{}
	clock := newFakeClock(baseTime)
	pruner := NewPruner(store, clock, PrunerConfig{Interval: time.Millisecond, MaxAge: time.Hour})

	sup := NewSupervisor(clock, discardLogger(), pruner)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Start is idempotent.
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}

	// Wait until the pruner has run at least once.
	deadline := time.After(2 * time.Second)
	for len(store.calls) == 0 {
		select {
		case <-deadline:
			t.Fatal("pruner never ran")
		case <-time.After(time.Millisecond):
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sup.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Stop is idempotent.
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// pluginsBlockingPlugin has a Stop that never returns, used to exercise the
// supervisor's deadline handling.
type pluginsBlockingPlugin struct {
	block chan struct{}
}

func (p *pluginsBlockingPlugin) Name() string                  { return "blocking" }
func (p *pluginsBlockingPlugin) Start(_ context.Context) error { return nil }
func (p *pluginsBlockingPlugin) Stop(_ context.Context) error  { <-p.block; return nil }

func TestSupervisorStopHonorsDeadline(t *testing.T) {
	sup := NewSupervisor(newFakeClock(baseTime), discardLogger(), &pluginsBlockingPlugin{block: make(chan struct{})})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// A context that is already done forces Stop to give up on the drain.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sup.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop err = %v, want context.Canceled", err)
	}
}

func TestSupervisorStopBeforeStart(t *testing.T) {
	sup := NewSupervisor(newFakeClock(baseTime), nil)
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("Stop before Start = %v, want nil", err)
	}
}
