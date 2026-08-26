// Copyright 2025 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package node

var (
	ValidatePublicAddress = validatePublicAddress
	UseEmbeddedSnapshot   = useEmbeddedSnapshot
)

// OptionalInt exposes the zero-means-unset conversion so the distinction it
// preserves can be tested. Getting it wrong turns an unset flag into a
// saturation limit of zero, which stops the node connecting to anyone.
var OptionalInt = optionalInt
