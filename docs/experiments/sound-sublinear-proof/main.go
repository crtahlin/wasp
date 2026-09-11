// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command sound-sublinear-proof tests whether a windowed order statistic gives a
// reserve-size proof that is both sound and sublinear, the question #248 left
// open. The proof takes an anchor-derived window covering a fraction of the
// address space, and thresholds on the k-th smallest transformed address among
// the chunks whose address falls in the window. See analysis.md.
//
// The model is scale-free. Only the count of a node's chunks in the window
// matters, so the simulation works in counts rather than materialising millions
// of chunks. The k-th smallest of c uniform transformed addresses is drawn as
// Erlang(k, c), which is its distribution for k much smaller than c: the sum of
// k unit exponentials divided by c. A smaller value means a denser window.
//
// Run: go run ./docs/experiments/sound-sublinear-proof
package main

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
)

// kthSmallest draws the k-th smallest of c uniform transformed addresses,
// Erlang(k, c). Returns +Inf when the window holds fewer than k chunks, which
// the threshold rejects.
func kthSmallest(rng *rand.Rand, c, k int) float64 {
	if c < k {
		return math.Inf(1)
	}
	g := 0.0
	for i := 0; i < k; i++ {
		g += -math.Log(rng.Float64())
	}
	return g / float64(c)
}

// poissonCount draws a window count for a reserve whose chunks are placed at
// random: the number of a node's chunks landing in a window is Binomial(size, w),
// well approximated by Poisson(size*w). A normal approximation is used for a
// large mean, for speed.
func poissonCount(rng *rand.Rand, mean float64) int {
	if mean > 30 {
		n := int(math.Round(mean + math.Sqrt(mean)*normal(rng)))
		if n < 0 {
			return 0
		}
		return n
	}
	l, k, p := math.Exp(-mean), 0, 1.0
	for {
		k++
		p *= rng.Float64()
		if p <= l {
			return k - 1
		}
	}
}

func normal(rng *rand.Rand) float64 {
	s := 0.0
	for i := 0; i < 12; i++ {
		s += rng.Float64()
	}
	return s - 6
}

// thresholdFor returns the acceptance threshold u for a window whose honest mean
// count is meanN, set so an honest full reserve passes with the target recall.
func thresholdFor(rng *rand.Rand, meanN float64, k, trials int, recall float64) float64 {
	hs := make([]float64, trials)
	for i := range hs {
		hs[i] = kthSmallest(rng, poissonCount(rng, meanN), k)
	}
	sort.Float64s(hs)
	return hs[int(recall*float64(trials))]
}

func main() {
	const (
		n      = 4_000_000 // honest reserve within radius, the bench scale
		k      = 16
		trials = 100_000
		recall = 0.95
		seed   = 1
	)
	out := os.Stdout

	fmt.Fprintf(out, "Windowed order statistic, honest reserve n=%d, k=%d, threshold for %.0f%% honest recall.\n\n", n, k, recall*100)

	// SOUNDNESS 1: the even-spacing attack from #248. A slacker holds a fraction
	// of the reserve; random placement is an ordinary smaller reserve, even
	// placement is the attack. Under the windowed statistic both give the same
	// window count, so even spacing must give no advantage, and the slacker must
	// be rejected below m=n. Shown across window sizes (cost is about n/f).
	fmt.Fprintln(out, "Soundness against even spacing: slacker pass rate by holdings and window size")
	fmt.Fprintf(out, "%-10s %-8s %-14s %-14s\n", "window", "m/n", "random pass", "even pass")
	for _, f := range []float64{50, 400, 2000} {
		w := 1 / f
		rng := rand.New(rand.NewSource(seed))
		u := thresholdFor(rng, float64(n)*w, k, trials, recall)
		for _, frac := range []float64{0.30, 0.50, 0.70, 0.90, 1.00} {
			meanM := frac * float64(n) * w
			var pr, pe int
			for t := 0; t < trials; t++ {
				if kthSmallest(rng, poissonCount(rng, meanM), k) < u {
					pr++
				}
				if kthSmallest(rng, int(math.Round(meanM)), k) < u { // even: near-fixed count
					pe++
				}
			}
			fmt.Fprintf(out, "1/%-8.0f %-8.2f %-14.4f %-14.4f\n", f, frac, float64(pr)/float64(trials), float64(pe)/float64(trials))
		}
	}

	// SOUNDNESS 2: the concentration attack. Because the window is unpredictable,
	// a node cannot pack its chunks where it will be measured. A node that stores
	// at full honest density but only over a fraction cov of the space (so it
	// holds cov*n chunks) is measured in a window that lands inside its covered
	// region only cov of the time. Its pass rate should be about cov, that is,
	// proportional to what it stores, not the pass-everything break even spacing
	// gave the bare probe scheme.
	fmt.Fprintln(out, "\nSoundness against concentration: pass rate of a node at full density over a fraction of the space")
	fmt.Fprintf(out, "%-14s %-14s\n", "coverage cov", "pass rate")
	{
		f := 400.0
		w := 1 / f
		rng := rand.New(rand.NewSource(seed))
		u := thresholdFor(rng, float64(n)*w, k, trials, recall)
		for _, cov := range []float64{0.25, 0.50, 0.75, 1.00} {
			var pass int
			for t := 0; t < trials; t++ {
				// the window lands inside the covered region with probability cov;
				// there it sees honest density, otherwise it is empty.
				var c int
				if rng.Float64() < cov {
					c = poissonCount(rng, float64(n)*w)
				} else {
					c = 0
				}
				if kthSmallest(rng, c, k) < u {
					pass++
				}
			}
			fmt.Fprintf(out, "%-14.2f %-14.4f\n", cov, float64(pass)/float64(trials))
		}
	}

	// COST AND SOUNDNESS TRADE. The window can shrink, cutting cost, only until it
	// holds too few chunks for the order statistic. For a fixed honest window
	// count C (the cost, in chunks read), report the pass rate of a half-share
	// even-spacing slacker. Soundness holds while C stays well above k; it
	// collapses as C approaches k, because both honest and slacker windows then
	// hold about k chunks and cannot be told apart.
	fmt.Fprintln(out, "\nCost and soundness: half-share even slacker pass rate versus window count C (cost)")
	fmt.Fprintf(out, "%-14s %-18s %-18s\n", "C (chunks)", "slacker pass (m=n/2)", "cost vs full pass")
	for _, C := range []float64{k * 1.0, k * 2.0, k * 4.0, k * 8.0, k * 16.0, k * 64.0, 2000} {
		rng := rand.New(rand.NewSource(seed))
		u := thresholdFor(rng, C, k, trials, recall)
		var pass int
		for t := 0; t < trials; t++ {
			if kthSmallest(rng, int(math.Round(C/2)), k) < u { // half-share, even -> fixed count C/2
				pass++
			}
		}
		fmt.Fprintf(out, "%-14.0f %-18.4f %-18s\n", C, float64(pass)/float64(trials), fmt.Sprintf("~n/%.0f", float64(n)/C))
	}

	fmt.Fprintln(out, "\nA half-share slacker should pass far below the honest 95%. Where it approaches 95% the")
	fmt.Fprintln(out, "window is too small to tell a half reserve from a full one; that sets the floor on C, and")
	fmt.Fprintln(out, "so the ceiling on the speedup n/C.")
}
