// Copyright 2026 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"syscall"

	"github.com/ethereum/go-ethereum/rpc"
)

// isTransportFailure reports whether an error means "this endpoint did not
// answer" rather than "this endpoint answered, and the answer is an error".
//
// The distinction is the whole design. Failing over on an answer would be wrong
// twice: a revert is a correct result, and retrying it elsewhere would hide a
// genuine contract or configuration problem behind a second opinion. See
// docs/experiments/rpc-endpoint-failover/spec.md.
func isTransportFailure(err error) bool {
	if err == nil {
		return false
	}

	// The caller gave up. Not the endpoint's fault, and moving on would mean
	// issuing a second request nobody is waiting for.
	if errors.Is(err, context.Canceled) {
		return false
	}

	// A JSON-RPC error is an answer. `execution reverted`, invalid params and
	// friends all arrive this way, and every one of them means the endpoint
	// worked.
	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		return false
	}

	// An HTTP-level error is the transport speaking, not the node. 5xx and 429
	// are worth moving on from; a 4xx other than 429 usually means we are
	// sending something wrong, and every endpoint would say the same.
	var httpErr rpc.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 500 || httpErr.StatusCode == 429
	}

	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isTransportFailure(urlErr.Err)
	}

	return false
}
