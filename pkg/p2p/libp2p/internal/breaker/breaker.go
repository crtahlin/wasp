// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package breaker

import (
	"errors"
	"sync"
	"time"
)

const (
	// defaults
	limit        = 100
	failInterval = 30 * time.Minute
	maxBackoff   = time.Hour
	backoff      = 2 * time.Minute
)

var (
	_ Interface = (*breaker)(nil)

	// ErrClosed is the special error type that indicates that breaker is closed and that is not executing functions at the moment.
	ErrClosed = errors.New("breaker closed")
)

type Interface interface {
	// Execute runs f() if the limit number of consecutive failed calls is not reached within fail interval.
	// f() call is not locked so it can still be executed concurrently.
	// Returns `ErrClosed` if the limit is reached or f() result otherwise.
	Execute(f func() error) error

	// ClosedUntil returns the timestamp when the breaker will become open again.
	ClosedUntil() time.Time
}

type currentTimeFn = func() time.Time

type breaker struct {
	limit                int // breaker will not execute any more tasks after limit number of consecutive failures happen
	consFailedCalls      int // current number of consecutive fails
	firstFailedTimestamp time.Time
	closedTimestamp      time.Time
	startBackoff         time.Duration // backoff to return to once a call succeeds
	backoff              time.Duration // current backoff duration, doubled on each re-trip
	maxBackoff           time.Duration
	failInterval         time.Duration // consecutive failures are counted if they happen within this interval
	currentTimeFn        currentTimeFn
	mtx                  sync.Mutex
}

type Options struct {
	Limit        int
	FailInterval time.Duration
	StartBackoff time.Duration
	MaxBackoff   time.Duration
}

func NewBreaker(o Options) Interface {
	return newBreakerWithCurrentTimeFn(o, time.Now)
}

func newBreakerWithCurrentTimeFn(o Options, currentTimeFn currentTimeFn) Interface {
	breaker := &breaker{
		limit:         o.Limit,
		startBackoff:  o.StartBackoff,
		backoff:       o.StartBackoff,
		maxBackoff:    o.MaxBackoff,
		failInterval:  o.FailInterval,
		currentTimeFn: currentTimeFn,
	}

	if o.Limit == 0 {
		breaker.limit = limit
	}

	if o.FailInterval == 0 {
		breaker.failInterval = failInterval
	}

	if o.MaxBackoff == 0 {
		breaker.maxBackoff = maxBackoff
	}

	if o.StartBackoff == 0 {
		breaker.startBackoff = backoff
		breaker.backoff = backoff
	}

	return breaker
}

func (b *breaker) Execute(f func() error) error {
	if err := b.beforef(); err != nil {
		return err
	}

	return b.afterf(f())
}

func (b *breaker) ClosedUntil() time.Time {
	b.mtx.Lock()
	defer b.mtx.Unlock()

	if b.consFailedCalls >= b.limit {
		return b.closedTimestamp.Add(b.backoff)
	}

	return b.currentTimeFn()
}

func (b *breaker) beforef() error {
	b.mtx.Lock()
	defer b.mtx.Unlock()

	// use currentTimeFn().Sub() instead of time.Since() so it can be deterministically mocked in tests
	if b.consFailedCalls >= b.limit {
		if b.closedTimestamp.IsZero() || b.currentTimeFn().Sub(b.closedTimestamp) < b.backoff {
			return ErrClosed
		}

		b.resetFailed()
		if newBackoff := b.backoff * 2; newBackoff <= b.maxBackoff {
			b.backoff = newBackoff
		} else {
			b.backoff = b.maxBackoff
		}
	}

	if !b.firstFailedTimestamp.IsZero() && b.currentTimeFn().Sub(b.firstFailedTimestamp) >= b.failInterval {
		b.resetFailed()
	}

	return nil
}

func (b *breaker) afterf(err error) error {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	if err != nil {
		if b.consFailedCalls == 0 {
			b.firstFailedTimestamp = b.currentTimeFn()
		}

		b.consFailedCalls++
		if b.consFailedCalls == b.limit {
			b.closedTimestamp = b.currentTimeFn()
		}

		return err
	}

	b.resetFailed()
	// A successful call means whatever was failing has recovered, so the
	// escalated backoff has served its purpose and is restored to its starting
	// value.
	//
	// Without this the backoff only ever grows. It doubles on each re-trip and
	// nothing ever lowers it, so a node that suffers a burst of dial failures
	// ratchets up to maxBackoff — an hour by default — and stays there for the
	// lifetime of the process, long after the network is healthy again. That
	// was observed on a bench node: 65 minutes with zero connected peers out of
	// 3198 known, while every mainnet bootnode was reachable by TCP from the
	// same host. Only a restart cleared it. See issue #74.
	b.backoff = b.startBackoff
	return nil
}

func (b *breaker) resetFailed() {
	b.consFailedCalls = 0
	b.firstFailedTimestamp = time.Time{}
}
