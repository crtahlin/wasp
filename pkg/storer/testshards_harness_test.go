// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build samplerharness

package storer_test

// testSharkyShards is the node's shard count in the samplerharness build,
// which opens real stores: with fewer, reads from the other shards fail and
// sharky's recovery can delete their index entries.
const testSharkyShards = 32
