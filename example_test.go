package oban_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/malcolmston/oban"
)

// Example demonstrates enqueuing a job and processing it with a registered
// worker, then draining the engine on shutdown.
func Example() {
	engine, err := oban.New(oban.Config{
		Store:        oban.NewInMemoryStore(),
		Queues:       map[string]int{"default": 2},
		PollInterval: time.Millisecond,
		Logger:       log.New(io.Discard, "", 0),
	})
	if err != nil {
		panic(err)
	}

	// The worker reports its result on a channel so the example output is
	// deterministic.
	greeted := make(chan string, 1)
	engine.RegisterFunc("greet", func(_ context.Context, job *oban.Job) error {
		var args struct {
			Name string `json:"name"`
		}
		if err := job.UnmarshalArgs(&args); err != nil {
			return err
		}
		greeted <- "Hello, " + args.Name
		return nil
	})

	ctx := context.Background()
	if err := engine.Start(ctx); err != nil {
		panic(err)
	}

	job, err := oban.NewJob("greet", map[string]string{"name": "Ada"})
	if err != nil {
		panic(err)
	}
	if _, _, err := engine.Enqueue(ctx, job); err != nil {
		panic(err)
	}

	fmt.Println(<-greeted)

	if err := engine.Stop(context.Background()); err != nil {
		panic(err)
	}
	// Output: Hello, Ada
}
