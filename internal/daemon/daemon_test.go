package daemon

import (
	"testing"
	"time"
)

func TestDrainInFlightCommandsWaitsForCompletionAndRejectsNewIntake(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			IdleTimeout: 100 * time.Millisecond,
		},
	}

	if err := d.beginCommand(); err != nil {
		t.Fatalf("beginCommand error: %v", err)
	}

	drained := make(chan struct{})
	go func() {
		d.drainInFlightCommands()
		close(drained)
	}()

	select {
	case <-drained:
		t.Fatal("drain returned before in-flight command finished")
	case <-time.After(20 * time.Millisecond):
	}

	if err := d.beginCommand(); err == nil {
		t.Fatal("expected beginCommand to reject new intake while draining")
	}

	d.endCommand()

	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not finish after in-flight command completed")
	}
}
