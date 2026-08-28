// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package log

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultSinkBuffer is how many rendered log lines may be waiting to be
	// written before further lines are discarded.
	//
	// Log lines here run to a few hundred bytes, so this is a couple of
	// megabytes at worst. It is meant to absorb a consumer that pauses, not
	// one that has gone away: a reader that has stopped for good will overrun
	// any buffer, and the point is that overrunning it costs log lines rather
	// than the node.
	DefaultSinkBuffer = 4096

	// drainTimeout bounds how long Close waits for buffered lines to reach the
	// writer.
	//
	// Draining matters because the lines immediately before shutdown are the
	// ones an operator needs. Bounding it matters more: an unbounded drain
	// would reintroduce issue #156 at shutdown, and a node that will not exit
	// is worse than one that loses its last few log lines.
	drainTimeout = 5 * time.Second
)

// AsyncSink writes to an io.Writer without letting that writer block its
// caller.
//
// It exists because pkg/log wrote to its sink synchronously and unboundedly.
// The production sink is os.Stdout, which under systemd, Docker or a desktop
// launcher is a pipe; when the reader stops, write(2) never returns. Since the
// peer-connection path logs, that block propagated into networking and a node
// was found holding zero peers, making zero dial attempts, and reporting
// healthy, for 105 minutes. See issue #156 and
// docs/experiments/log-sink-backpressure/spec.md.
type AsyncSink struct {
	w     io.Writer
	lines chan []byte

	dropped atomic.Uint64
	// reported is how many drops have already been announced, so the notice
	// covers each stall episode once rather than repeating a running total.
	reported atomic.Uint64

	closeOnce sync.Once
	closed    atomic.Bool
	done      chan struct{}
}

var _ io.Writer = (*AsyncSink)(nil)

// NewAsyncSink returns a sink that hands lines to w from its own goroutine.
//
// A capacity of zero or less returns nil, which callers take to mean "write
// synchronously". That is the escape hatch for a caller whose writer must be
// observed immediately, such as a test comparing exact output.
func NewAsyncSink(w io.Writer, capacity int) *AsyncSink {
	if capacity <= 0 {
		return nil
	}

	s := &AsyncSink{
		w:     w,
		lines: make(chan []byte, capacity),
		done:  make(chan struct{}),
	}
	go s.drain()
	return s
}

// Write queues p. It never blocks, and it never reports failure.
//
// Returning an error here would be worse than useless. Every level method in
// this package does
//
//	if err := l.log(...); err != nil { fmt.Fprintln(os.Stderr, err) }
//
// so an error on a dropped line becomes an unbounded blocking write to stderr,
// and under `bee ... 2>&1 | consumer` that is the same stalled pipe. The
// deadlock would simply move into the error path, where it is harder to find.
func (s *AsyncSink) Write(p []byte) (int, error) {
	if s.closed.Load() {
		s.dropped.Add(1)
		return len(p), nil
	}

	// The caller owns p and may reuse it, so the queued copy must be ours.
	line := make([]byte, len(p))
	copy(line, p)

	select {
	case s.lines <- line:
	default:
		s.dropped.Add(1)
	}
	return len(p), nil
}

// Dropped reports how many lines have been discarded over the sink's life.
func (s *AsyncSink) Dropped() uint64 { return s.dropped.Load() }

// Close stops the sink, draining what is buffered until the writer takes it or
// drainTimeout elapses.
//
// Safe to call more than once, and Write after Close is safe too: a node logs
// while it shuts down, and neither should panic on a closed channel.
func (s *AsyncSink) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.lines)

		select {
		case <-s.done:
		case <-time.After(drainTimeout):
			err = fmt.Errorf("log sink: drain timed out after %s with %d lines buffered",
				drainTimeout, len(s.lines))
		}
	})
	return err
}

// drain moves queued lines to the writer, one at a time, in order.
func (s *AsyncSink) drain() {
	defer close(s.done)

	for line := range s.lines {
		// A blocking write here is the whole point: this goroutine is the one
		// allowed to wait, precisely so that no other goroutine has to.
		_, _ = s.w.Write(line)
		s.reportDrops()
	}
}

// reportDrops writes a notice for lines discarded since the last one.
//
// Without it the new behaviour is silent log loss, which is a milder member of
// the same family as the defect being fixed: an instrument that is present and
// says nothing. The notice goes into the log stream itself, where whoever is
// reading the gap will see it.
//
// Called after a successful write, so it only runs when the writer is moving
// again, which is also when the stall is over and the count is final.
func (s *AsyncSink) reportDrops() {
	dropped := s.dropped.Load()
	reported := s.reported.Load()
	if dropped == reported {
		return
	}
	if !s.reported.CompareAndSwap(reported, dropped) {
		return
	}
	_, _ = fmt.Fprintf(s.w, "dropped %d log lines: the log consumer was not reading\n",
		dropped-reported)
}

// asyncSinks maps an underlying writer to the AsyncSink that serves it.
//
// One wrapper per writer, so a process whose loggers all share os.Stdout has
// exactly one drain goroutine no matter how many loggers it builds. Keyed by
// the writer itself, which is why the value is only ever an interface holding
// a pointer in practice.
var asyncSinks sync.Map

// asyncSinkFor returns the sink to write through for w.
//
// Only an *os.File is wrapped by default, and that is deliberate. The failure
// in issue #156 is write(2) blocking on a file descriptor whose reader has
// stopped: a pipe, a socket, a full disk. That is exactly what an *os.File is,
// and it is what the production sink is — cmd.OutOrStdout() gives os.Stdout.
//
// Anything else is left alone. The logger cannot know whether an arbitrary
// writer blocks, and buffering one changes a contract callers rely on: that
// the line has reached the sink by the time the log call returns. That is not
// hypothetical. Wrapping every writer broke Example, TestNewLoggerWithTraceID,
// TestRetrieveChunk and TestStartSpanFromContext_logger, all of which log and
// then immediately assert on a bytes.Buffer.
//
// A caller with a writer that can block and is not a file asks for buffering
// with WithSinkBuffer, which sets forced.
func asyncSinkFor(w io.Writer, buffer int, forced bool) io.Writer {
	if w == nil || buffer <= 0 {
		return w
	}
	if _, isFile := w.(*os.File); !isFile && !forced {
		return w
	}
	// Already wrapped. A caller that built its own sink, to control when the
	// drain goroutine stops, must not have a second one layered on top: that
	// would be two goroutines and two hops for every line.
	if s, ok := w.(*AsyncSink); ok {
		return s
	}
	if existing, ok := asyncSinks.Load(w); ok {
		return existing.(*AsyncSink)
	}
	s := NewAsyncSink(w, buffer)
	actual, loaded := asyncSinks.LoadOrStore(w, s)
	if loaded {
		// Another goroutine got there first. Close ours rather than leaking
		// its drain goroutine.
		_ = s.Close()
		return actual.(*AsyncSink)
	}
	return s
}

// CloseAsyncSinks drains and stops every sink this package created.
//
// Logger is an interface with no Close, and adding one would break every
// implementation, so shutdown reaches the sinks through here instead.
func CloseAsyncSinks() error {
	var err error
	asyncSinks.Range(func(_, v any) bool {
		if cerr := v.(*AsyncSink).Close(); cerr != nil && err == nil {
			err = cerr
		}
		return true
	})
	return err
}
