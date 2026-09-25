// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/ethersphere/bee/v2/cmd/bee/cmd"
	"github.com/ethersphere/bee/v2/pkg/node"
)

// exitConfig is the status for a configuration error, EX_CONFIG from
// sysexits.h. The service unit sets RestartPreventExitStatus to it, so a value
// a restart cannot fix stops the node instead of looping (issue #490).
//
// 78 rather than a number of our own: it is the conventional meaning, and it
// does not collide with the 128+signal range or with 1, which stays the status
// for every other failure so nothing that restarts today stops restarting.
const exitConfig = 78

// exitCode maps an error from the command to a process status. Split out from
// main so it can be tested: the whole point of the status is what systemd does
// with it, and running the binary to find out is not a unit test.
func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, node.ErrConfig):
		return exitConfig
	default:
		return 1
	}
}

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(exitCode(err))
	}
}
