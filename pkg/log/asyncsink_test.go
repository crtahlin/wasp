// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package log

import (
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// stalledWriter blocks every Write until release is closed, then records what
// it was given.
//
// The block is a channel receive on purpose. testing/synctest treats a blocked
// channel receive as durably blocked, which is what lets synctest.Wait observe
// the stall and return. A mutex would not be durably blocking, and the test
// would deadlock the whole bubble instead of failing with a message.
type stalledWriter struct {
	release chan struct{}

	mu    sync.Mutex
	lines [][]byte
}

func newStalledWriter() *stalledWriter {
	return &stalledWriter{release: make(chan struct{})}
}

func (w *stalledWriter) Write(p []byte) (int, error) {
	<-w.release

	w.mu.Lock()
	defer w.mu.Unlock()
	// Copy: a sink is entitled to reuse the buffer it hands us, and a
	// non-copying fake would quietly assert against aliased memory.
	w.lines = append(w.lines, append([]byte(nil), p...))
	return len(p), nil
}

// restoreGlobals saves and restores the package's global registry state.
//
// NewLogger pins the default options through a sync.Once and stores loggers in
// a process-wide map. A test that calls it without restoring both leaks into
// every later test in the package: pinning the defaults made Example silently
// log nowhere, because its own ModifyDefaults call became a no-op. That is why
// this file is an internal test rather than a log_test one.
func restoreGlobals(t *testing.T) {
	t.Helper()

	l, o := loggers, defaults.options
	t.Cleanup(func() {
		loggers = l
		defaults.pin = sync.Once{}
		defaults.options = o
		asyncSinks = sync.Map{}
	})
}

// TestLoggerDoesNotBlockOnStalledSink is the regression test for issue #156.
//
// A stock node was found holding zero peers with zero dial attempts for 105
// minutes, reporting healthy throughout. Seven dial goroutines were blocked
// writing a log line, and kademlia's manage loop was blocked waiting for them.
// The sink was os.Stdout, which under systemd, Docker or a desktop launcher is
// a pipe: when the reader stops, write(2) never returns.
//
// The property is that logging must not be able to stop the goroutine that
// logs. Written against the public API only, so it compiles and fails on the
// unfixed tree rather than being a test of machinery that does not exist yet.
func TestLoggerDoesNotBlockOnStalledSink(t *testing.T) {
	restoreGlobals(t)

	var finished bool

	synctest.Test(t, func(t *testing.T) {
		// Everything that blocks must be created inside the bubble.
		w := newStalledWriter()

		lg := NewLogger(t.Name(),
			WithSink(w),
			// Asked for explicitly because this writer is a fake, not an
			// *os.File. Only files are buffered by default; see asyncSinkFor.
			// TestFileSinkIsBufferedByDefault covers the production shape.
			WithSinkBuffer(DefaultSinkBuffer),
			WithVerbosity(VerbosityDebug),
		).Build()

		done := make(chan struct{})
		go func() {
			defer close(done)
			// Many, not one. A single call could return on a store with any
			// buffering at all; the test must exhaust whatever capacity the
			// implementation has.
			for i := range 1000 {
				lg.Info("dial attempt", "i", i)
			}
		}()

		// Returns once every other bubbled goroutine is durably blocked or
		// finished. Either the logging goroutine got through all 1000 calls,
		// or it is stuck in the sink.
		synctest.Wait()

		select {
		case <-done:
			finished = true
		default:
		}

		// Release, then stop the sink, before returning. synctest requires
		// every bubbled goroutine to have exited before the bubble closes, and
		// the drain goroutine sits on its queue for ever otherwise. This is
		// what cmd/bee does at shutdown, for the same reason.
		//
		// Failing in here would panic the bubble on the way out and bury the
		// message, so the assertion happens after it instead.
		close(w.release)
		if err := CloseAsyncSinks(); err != nil {
			t.Error(err)
		}
		synctest.Wait()
	})

	if !finished {
		t.Fatal("logging blocked on a stalled sink: the goroutine did not " +
			"finish its Info calls, so a stalled log consumer can stop a " +
			"node doing work (issue #156)")
	}
}

// TestFileSinkIsBufferedByDefault pins the production path.
//
// The other tests here pass WithSinkBuffer explicitly, because their sinks are
// fakes. This one uses the shape a node actually has — an *os.File whose reader
// has stopped, which is what os.Stdout is under systemd, Docker or a desktop
// launcher — and asks for nothing. If the default ever stops covering it, an
// operator gets issue #156 back and no other test here would notice.
func TestFileSinkIsBufferedByDefault(t *testing.T) {
	restoreGlobals(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })

	// No WithSinkBuffer: this must work because w is a file.
	lg := NewLogger(t.Name(), WithSink(w), WithVerbosity(VerbosityDebug)).Build()
	t.Cleanup(func() { _ = CloseAsyncSinks() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than the pipe holds, so the writer is stalled for most of
		// them. Nothing reads r.
		for i := range 20000 {
			lg.Info("dial attempt", "i", i)
		}
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("logging to an unread pipe blocked: the default no longer " +
			"protects the production sink shape (issue #156)")
	}
}
