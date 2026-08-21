// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bee

import (
	"strconv"
	"time"
)

var (
	version    string // automatically set semantic version number
	commitHash string // automatically set git commit hash
	commitTime string // automatically set git commit time

	// upstreamBase is the ethersphere/bee release tag this fork is currently
	// synced to, injected at build time from the repo-root .upstream-base file.
	// This fork carries its own semver line, so this is the only way an operator
	// (or a peer reading the libp2p user agent) can tell which upstream code
	// base is actually running.
	upstreamBase string

	Version = func() string {
		if commitHash != "" {
			return version + "-" + commitHash
		}
		return version + "-dev"
	}()

	// UpstreamBase returns the ethersphere/bee release this build derives from,
	// or "unknown" when the binary was not built through the Makefile.
	UpstreamBase = func() string {
		if upstreamBase == "" {
			return "unknown"
		}
		return upstreamBase
	}()

	// CommitTime returns the time of the commit from which this code was derived.
	// If it's not set (in the case of running the code directly without compilation)
	// then the current time will be returned.
	CommitTime = func() string {
		if commitTime == "" {
			commitTime = strconv.Itoa(int(time.Now().Unix()))
		}
		return commitTime
	}
)
