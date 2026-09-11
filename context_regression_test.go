package tgtg

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultPINReaderPreservesBufferedInput(t *testing.T) {
	in, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	t.Cleanup(func() {
		os.Stdin = old
		in.Close()
		out.Close()
	})
	if _, err := io.WriteString(out, "1234\n5678\n"); err != nil {
		t.Fatal(err)
	}
	out.Close()
	c := New(Config{UserAgent: "ua", Output: io.Discard})
	for _, want := range []string{"1234\n", "5678\n"} {
		got, err := c.readPIN(context.Background())
		if err != nil || got != want {
			t.Fatalf("default PIN = %q, want %q; error=%v", got, want, err)
		}
	}
}

func TestCanceledLegacySleepDoesNotReplaceFreshDelay(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	c := New(Config{UserAgent: "ua", Output: io.Discard, Sleep: func(time.Duration) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- c.wait(ctx, time.Second) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sleep callback did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled wait did not return")
	}
	// An abandoned callback still occupies the only legacy sleep slot.
	next, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := c.wait(next, time.Second)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("waiting for abandoned sleep: error=%v calls=%d", err, calls.Load())
	}
	unblock()
	next, stop = context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := c.wait(next, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("new delay reused canceled sleep: calls=%d, want 2", got)
	}
}
