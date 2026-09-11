package tgtg

import (
	"context"
	"fmt"
	"time"
)

type pinResult struct {
	pin string
	err error
}

// awaitCallback retains at most one unfinished legacy callback. Cancellation
// returns promptly while leaving its result available to a later call.
func awaitCallback[T any](ctx context.Context, pending *<-chan T, call func() T) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if *pending == nil {
		result := make(chan T, 1)
		*pending = result
		go func() { result <- call() }()
	}
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case result := <-*pending:
		*pending = nil
		return result, ctx.Err()
	}
}

func (c *Client) readPIN(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.pinReaderContext != nil {
		pin, err := c.pinReaderContext(ctx)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return pin, err
	}
	if c.promptPIN && c.pendingPIN == nil {
		fmt.Fprint(c.out, "Enter PIN from email: ")
	}
	reader := c.pinReader
	result, err := awaitCallback(ctx, &c.pendingPIN, func() pinResult {
		pin, err := reader()
		return pinResult{pin: pin, err: err}
	})
	if err != nil {
		return "", err
	}
	return result.pin, result.err
}

// discardPendingPIN waits for an abandoned account's reader without starting
// another reader or using its result for the new account.
func (c *Client) discardPendingPIN(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.pendingPIN:
		c.pendingPIN = nil
		c.pendingPINEmail = ""
		c.pendingPINPollingID = ""
		return ctx.Err()
	}
}

func (c *Client) wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.sleepContext != nil {
		err := c.sleepContext(ctx, delay)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	// A retained callback belongs to a canceled wait. Let it finish before
	// starting this delay, so an earlier sleep cannot shorten a new one.
	if c.pendingSleep != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.pendingSleep:
			c.pendingSleep = nil
		}
	}
	sleep := c.sleep
	_, err := awaitCallback(ctx, &c.pendingSleep, func() struct{} {
		sleep(delay)
		return struct{}{}
	})
	return err
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
