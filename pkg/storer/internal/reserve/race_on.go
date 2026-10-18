// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build race

package reserve

// raceEnabled makes the verified chunk type check run on every call in race
// builds.
const raceEnabled = true
