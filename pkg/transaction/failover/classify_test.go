// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethersphere/bee/v2/pkg/transaction/failover"
)

// jsonRPCError is what an endpoint returns when it worked and the chain said no.
type jsonRPCError struct{ msg string }

func (e jsonRPCError) Error() string  { return e.msg }
func (e jsonRPCError) ErrorCode() int { return 3 }

var _ rpc.Error = jsonRPCError{}

// TestClassification pins the distinction the whole design rests on: an
// endpoint that did not answer is replaced, an endpoint that answered is
// believed. Getting a single row of this wrong either hides a real contract
// error behind a second opinion, or strands the node on a dead endpoint.
func TestClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		err       error
		transport bool
		why       string
	}{
		{"nil", nil, false, "no error is not a failure"},
		{"connection refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, true, ""},
		{"connection reset", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, true, ""},
		{"host unreachable", &net.OpError{Op: "dial", Err: syscall.EHOSTUNREACH}, true, ""},
		{"dns failure", &net.DNSError{Err: "no such host"}, true, ""},
		{"eof", io.EOF, true, "the endpoint hung up mid-response"},
		{"unexpected eof", io.ErrUnexpectedEOF, true, ""},
		{"deadline exceeded", context.DeadlineExceeded, true, "the endpoint did not answer in time"},
		{"url error wrapping a dial failure", &url.Error{Op: "Post", Err: syscall.ECONNREFUSED}, true, ""},
		{"wrapped dial failure", fmt.Errorf("dialing: %w", syscall.ECONNREFUSED), true, ""},

		{"context canceled", context.Canceled, false,
			"the caller gave up; re-issuing to another endpoint serves nobody"},
		{"json-rpc error", jsonRPCError{"execution reverted"}, false,
			"a revert is a correct answer; a second opinion would hide a real problem"},
		{"http 500", rpc.HTTPError{StatusCode: 500}, true, "the transport failed, not the chain"},
		{"http 503", rpc.HTTPError{StatusCode: 503}, true, ""},
		{"http 429", rpc.HTTPError{StatusCode: 429}, true,
			"rate limited here, so another endpoint is worth trying"},
		{"http 400", rpc.HTTPError{StatusCode: 400}, false,
			"we sent something wrong; every endpoint would say the same"},
		{"http 401", rpc.HTTPError{StatusCode: 401}, false,
			"an auth problem is configuration, not availability"},
		{"plain error", errors.New("something went wrong"), false,
			"unrecognised errors are treated as answers, so an unknown case cannot " +
				"silently strand the node moving between endpoints"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := failover.IsTransportFailure(c.err)
			if got != c.transport {
				msg := fmt.Sprintf("classified as transport=%v, want %v", got, c.transport)
				if c.why != "" {
					msg += ": " + c.why
				}
				t.Error(msg)
			}
		})
	}
}
