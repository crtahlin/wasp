# Hosting a directory or website without a stamp: results

Issue: [#340](https://github.com/crtahlin/wasp/issues/340). Spec:
[directory-ingest.md](directory-ingest.md), merged as
[`ef00d3eb`](https://github.com/crtahlin/wasp/commit/ef00d3eb). Implementation
merged as [`d32cb541`](https://github.com/crtahlin/wasp/commit/d32cb541).

Measured 2026-09-19 on the two-node bench, `bench-1` as the provider and
`bench-2` as the requester. **Arms 1 to 6 were run by hand**, with every command
and its output recorded in `cp290/t19-directory-ingest.txt`; there is no
`t19.sh`, and an earlier version of this line claimed one. The arm 5 and arm 6
re-runs were scripted, as `cp290/t19b.sh` and `cp290/t19c.sh` with rows in
`t19b-arm6-rerun.txt` and `t19c-arm56-drift.txt`. All of those live outside this
repository per rule 10.

**Terms.** A **collection** is a directory stored with the manifest that maps a
path to the file at that path. **The reported count** is the `chunks` field of
the ingest response. **`TotalChunks`**, **`SharedSlots`** and
**`ReferenceCount`** are the three `ChunkStore` counters `/debugstore` exposes
(`pkg/storer/debug.go`): distinct chunk entries, slots shared by more than one
reference, and total references. A **control window** is an equal period read
immediately before a measured one, showing how far the counters drift with the
node doing nothing.

**Five of the six arms pass. The sixth, arm 4b, does not meet its condition.**

That count needs two qualifications before it is quoted, because two of the five
did not pass as first run. **Arm 4 passes only after its precondition was
corrected**, having returned the opposite result against the spec as merged. And
**arm 5 passes only under a rule this change rewrites**, twice: the merged rule
compared against the wrong counter, and the first correction then demanded an
exactness the node's own drift does not allow. Neither is a code defect, and both
are set out below, but "five of six" reads like five clean passes and they are
not that.

What passes: a directory ingested with no postage produces the same manifest
root as a stamped upload of the same archive, the holder serves every path in it,
a node with no hint cannot reach it, the reported chunk count is exact once
the measurement window is kept tight, and an over-limit archive is refused
without residue.

What does not: arm 4b asks that a node naming the holder be served every inner
path with a matching hash **in all three runs**. It was not. The one multi-chunk
file in the collection returned HTTP 200 with an **empty body** in 2 of 13
attempts. Every single-chunk **inner** path was correct 12 of 12, and **inside
the gated pass alone, where nothing else differs at all, three single-chunk paths
were correct 9 of 9 while the multi-chunk file was 1 of 3.**

That failure is the signature
[#313](https://github.com/crtahlin/wasp/issues/313) reports and
[#343](https://github.com/crtahlin/wasp/issues/343) traced, on a path both issues
leave open: a chunk refused credit is dropped from that chunk's preferred path,
falls to peers that never held it, and `joiner.ReadAt` is all or nothing, so one
lost chunk loses the whole read unit.

**Two facts put the failure downstream of everything this change added**, and
both are stronger than the hit-rate contrast:

- **A 200 with an empty body means the response headers were already written.**
  So the manifest had been resolved and the path lookup had succeeded before
  anything went wrong. A manifest fault gives 404 **before** headers, which is
  what the no-hint arm returns. Whatever failed, failed after the manifest did
  its work.
- **The holder served a ten-chunk file correctly.** `/img/pixel.bin` in arms 2
  and 3 is 40,960 bytes and was returned with a matching hash. So multi-chunk
  resolution through a manifest works; chunk count alone is not the variable. The
  variable is chunk count **over the remote hinted path**.

Nothing measured here puts the failure in #340's code, and nothing measured here
proves the attribution either. It is stated as consistent with those issues, not
as established. The consequence for this experiment is that **hosting a
multi-chunk file for a remote reader is not yet shown to work reliably**,
whatever the manifest does correctly.

**Four of the spec's rules were found wrong, every one of them only by running
it.** Three were in the spec as merged: one would have rejected correct code, one
made an arm test something other than what it claimed, and one measured nothing
at all. The fourth was written **here**, to fix the first, and was stricter than
the node allows.

**This document then made three further errors of its own, reporting those
fixes.** It announced a correction to the spec that had not actually been made; it
withdrew a claim on evidence that does not support the withdrawal; and it fitted a
drift rate to the number it was explaining rather than checking it. All three are
set out below rather than quietly repaired, because the pattern in every one of
them is the same: **each error made a rule or a finding look sharper than the
evidence allowed.**

That count went from two to four to seven across three review rounds, and every
addition was found by someone applying a rule to a row rather than reading the
sentence describing it. That is the transferable lesson of this experiment, more
than anything it establishes about directories.

Two faults in the measuring script have their own section too, one of which had
already reported arm 4b as a pass.

## The provider was running the wrong build, and the first arm failed against it

Recorded first because it nearly produced a false refutation.

The provider was on `0.1.3-f005605d`, the build from the #326 branch, which has
no #340 code at all and therefore ignores `Swarm-Collection` and stores the
archive as a single blob. Arm 1 ran against it and the roots differed.

That was not read as a result, because the ingest reported 19 chunks for a
five-file site, too few for a manifest. **Proved rather than assumed**: the same
tar ingested with the header and without it returned the identical reference,
`6b16797b6648bcdc31d6ad11f1745bfe2e9f18b3fa9ec84bdfab799b4a8b87e6`, so the
header was being ignored.

Redeployed from `main` at `d372367f` as `0.1.3-340-d372367f`, with the unit, the
process and the version read back after the restart rather than the port, which
is what `deploy-326.sh` warns about in its own header. Arm 1 then passed at the
first attempt. The requester stayed on `0.1.3-353-5e527a22`: this is a
provider-side feature.

## Preconditions, measured

`ChunkStore.TotalChunks` was **flat across four consecutive 20-second windows**
before anything ran, which is what arms 5 and 6 need and what
[local-ingest-results.md](local-ingest-results.md) records an earlier pass
failing to have. Local ingest usage was 119,063 of 131,072, so **12,009 chunks
of headroom** at the start. By the time arm 6 ran, arms 1 and 5 had consumed 781
of that and the headroom was **11,228**, which is the figure that matters there
and the one arm 6 quotes. The provider grant was zero on both settings.

## What was measured

### 1. Manifest equivalence, on one node

| Archive | Ingested root | Chunks | Stamped root | Equal |
|---|---|---|---|---|
| site | `3257e80e...11927b0c` | 53 | `3257e80e...11927b0c` | yes |
| site2 | `245f07cf...bd98206c` | 35 | `245f07cf...bd98206c` | yes |

Both sides on the provider, same redundancy level, same index and error
document. 53 chunks against 19 for the same tar stored as a blob, which is the
manifest path doing work.

Scoped to one node deliberately. A manifest is not a pure function of the bytes
the way a blob reference is: for a tar the entry content type comes from
`mime.TypeByExtension`, whose table Go reads from files on the host. A cross-host
run is a separate question and is not claimed here.

### 2 and 3. The holder serves the root and every path

Six of six, every SHA-256 matching the local original, including the bare root
resolving through the index document.

| Path | HTTP | Bytes | SHA matches original |
|---|---|---|---|
| `/` | 200 | 95 | yes, the index document |
| `/index.html` | 200 | 95 | yes |
| `/css/style.css` | 200 | 44 | yes |
| `/a/b/note.txt` | 200 | 29 | yes |
| `/img/pixel.bin` | 200 | 40,960 | yes |
| `/404.html` | 200 | 9 | yes |

### 4. A second node with no hint

404 for the root and for an inner path, in all three runs. Times, root and inner
path per run: 5.64 and 7.52, 1.80 and 1.80, 1.80 and 1.80 seconds. The first run
of any sequence is slower, which is consistent across every arm here.

**This arm failed on the first attempt for a reason that is the spec's, not the
node's**, and that is the first of the two spec defects below.

### 4b. A second node that names the holder. Condition not met

Two passes over the same never-stamped collection, three runs each. The second
pass is the one that counts, because it reads `provider_connected` back as 1
before making any request and the first pass did not gate on that at all.

Correct below means all three of: HTTP 200, the exact byte count, and a SHA-256
matching the local original. **Status alone is not correct**, which is the second
harness fault recorded further down.

| Path | Chunks | First pass, ungated | Second pass, gated |
|---|---|---|---|
| `/` bare root, through the index document | 1 | 2 of 3 | 3 of 3 |
| `/css/style.css` | 1 | 3 of 3 | 3 of 3 |
| `/a/b/note.txt` | 1 | 3 of 3 | 3 of 3 |
| `/blob.bin` | **4** | 3 of 3 | **1 of 3** |

The two `/blob.bin` failures were **HTTP 200 with an empty body**: zero bytes
returned for a 16,384-byte file, and the harness first scored them as successes.

**Per-path times were not recorded for either path sequence**, which is a gap in
the rows rather than a result. The spec asks these arms for a spread over elapsed
time, and only the standalone runs below and the root-only runs carry it. Any
repeat of this arm should record the time of every fetch.

Six further runs of `/blob.bin` on its own, with the overdraft counter read
either side of each, were all correct:

| Run | HTTP | Bytes | Seconds | curl exit | SHA | `preferred_overdrafts` |
|---|---|---|---|---|---|---|
| 1 | 200 | 16,384 | 0.248 | 0 | matches | 0 |
| 2 | 200 | 16,384 | 0.248 | 0 | matches | 0 |
| 3 | 200 | 16,384 | 0.247 | 0 | matches | 0 |
| 4 | 200 | 16,384 | 0.411 | 0 | matches | **+16** |
| 5 | 200 | 16,384 | 0.247 | 0 | matches | 0 |
| 6 | 200 | 16,384 | 0.747 | 0 | matches | **+11** |

Two of those six took credit refusals and still returned the whole file, so a
refusal is not sufficient on its own to lose the content.

**Tally for the multi-chunk file: 11 correct of 13.** The thirteen are the three
of the first pass, the three of the gated pass, the six standalone runs above,
and **one further standalone fetch made while diagnosing the first harness
fault**, which is the one not in any table here; it was correct. Ten of the
twelve tabulated attempts succeeded, and the diagnostic fetch is the eleventh
success.

Both failures fell in one sequence, as the fourth request of four in rapid
succession. **Position is not the cause, and the cleanest evidence is inside the
gated pass itself:** run 3 fetched the file in that same fourth position and
returned all 16,384 bytes with a matching hash. The first pass did the same three
times. What separates the successful conditions from the failing one is at most
timing, and the evidence does not distinguish timing from chance at this sample
size.

**The bare root failed once in the twelve attempts tabulated above**, in run 3 of
the first pass, at first contact and in a sequence with no connectivity gate. A
**second** root failure occurred earlier still, in the pass whose rows were set
aside because of the first harness fault, and that one ran with the provider read
back as **not connected**. It is mentioned because it is the case that carries
the connectivity evidence, and it is kept out of the count because rows from a
discarded pass cannot be counted when they help and ignored when they do not. So
the defensible figure is **one failure in twelve**, with a second in a pass that
is not being counted.

**Those two failures are a different mode and are not pooled with the empty
bodies.** The root failed with **404**, which is the path not resolving. The
multi-chunk file failed with **200 and no bytes**, which is the path resolving
and the content not arriving. Pooling them would hide the one observation this
arm contributes.
`Wasp-Providers` connects in the background and a preferred candidate is filtered
to connected peers, so a first request can be made before the provider is usable.
That is the likeliest cause and it is **correlational, not established**: it
would take a run that disconnects the provider deliberately and then makes one
hinted request to settle it. The gated pass had the root 3 of 3 and six
root-only runs were 6 of 6, two of them with `preferred_overdrafts` rising by 12
and by 3 and both still serving, because a refusal falls through to ordinary
selection.

**Why this arm is reported as not met rather than as a pass with a caveat.** The
spec's Accept condition 4 wants every inner path served correctly in all three
runs, and its catch-all clause rejects anything the accept conditions do not
cover. Two empty bodies in a qualifying, gated pass is a reject under both. The
single-chunk inner paths passing 12 of 12 does not rescue it, because a website
is made of files of arbitrary size and the only multi-chunk file in the fixture
is the one that failed. Reporting it as a pass with a caveat would be the same
mistake as scoring on the status code.

### 5. Does the count cover the manifest

| Run | Control drift | Reported | `TotalChunks` rise | `SharedSlots` | `ReferenceCount` rise |
|---|---|---|---|---|---|
| 1 | 0 | 206 | 203 | +3 | **206** |
| 2 | 0 | 206 | 203 | 0 | **206** |
| 3 | 0 | 206 | 203 | 0 | **206** |

A second pass was added after review, on 303-chunk collections, with the control
window matched in length to the ingest and the ingest itself under 0.12 seconds:

| Run | Control drift | Reported | `TotalChunks` rise | `ReferenceCount` rise |
|---|---|---|---|---|
| 1 | 0 | 303 | 300 | **303** |
| 2 | 0 | 303 | 300 | **303** |
| 3 | 0 | 303 | 300 | **303** |

**The reported count equals the `ReferenceCount` rise exactly in all six runs**,
and is short against `TotalChunks` by exactly 3 in all six. That shortfall is not
a miscount. Two diagnostics locate it:

| Kind | Reported | `TotalChunks` rise | `ReferenceCount` rise | Shortfall |
|---|---|---|---|---|
| blob, 225,280 bytes | 65 | 65 | 65 | **0** |
| collection, one small file and an index | 26 | 23 | 26 | **3** |

So the shortfall is **3 on every collection measured directly**, at 26, 206 and
303 chunks, and **0 for a blob**. Those are manifest chunks the node already
held, and `TotalChunks` cannot rise for a chunk that is already stored.
`SharedSlots` rising by 3 on the first such ingest and by 0 afterwards is those
slots going from one reference to two, and then to three.

**The blob's zero is a prediction, not a fourth measurement.** A blob has no
manifest, so there are no shared manifest nodes for the node to already hold, and
0 is the only value consistent with the explanation. That is what moves this off
correlation: the mechanism forbids the blob from behaving like the collections,
and it does not.

**An earlier version of this document said the shortfall was a constant 3, and
withdrew it on evidence that does not actually support the withdrawal.** Arm 6
run 1 of the first pass ingested about 5,841 chunks with a shortfall that works
out at **9**, derived rather than recorded: the run's reported count was never
written down, so it is reconstructed from the headroom before it (11,228, giving
held = 119,844) and the held figure the later refusals report (125,685), against
a `TotalChunks` rise of 5,832.

**That 9 is uninformative, and saying otherwise was a second error.** The
shortfall measured this way is `reported - TotalChunks rise`, which the model
below makes `S - d`, so a reading of 9 is equally what S = 3 gives when drift is
**minus 6**, and a cache eviction inside the window is exactly that. The figure
is neutral between "S grows with collection size" and "S is 3 and the window
drifted downwards", and it cannot decide between them.

What stands without it is the reason not to predict the shortfall at all: a
shallow manifest trie shares few canonical nodes with every other manifest and a
deeper one shares more, so 3 is a property of these archives rather than a
constant of the design. The spec records it per run instead of predicting it, and
the invalidation clause that depended on a constant is removed.

### What drifts is the measurement window, and an earlier version of this document blamed the ingest

A seventh run, the first attempt at the second pass, gave reported **303**,
`TotalChunks` **306** and `ReferenceCount` **309**, against a control window that
read 0.

`TotalChunks` cannot rise by more than the reported count on its own, because the
ingest can create at most one new entry per distinct address it reports. So the
excess is background traffic. Writing N for the archive's distinct addresses, S
for the ones the node already held and d for that traffic:

```
reported            = N         = 303
TotalChunks rise    = N - S + d = 306
ReferenceCount rise = N + d     = 309
```

**d is read, not fitted.** The ingest contributes exactly N to `ReferenceCount`,
one increment per distinct address, so `d = 309 - 303 = 6` follows from the
`ReferenceCount` row alone with no model at all. S = 3 then follows **only if the
same d applies to `TotalChunks`**, which is an assumption about what background
traffic does and not a reading: a Put of a chunk the node already holds moves
`ReferenceCount` and leaves `TotalChunks` still, which would break it.

That assumption is measured five separate times in the two re-run files, and
every one supports it: control windows of 13/13, 7/7 and 1/1, plus two windows in
which nothing was ingested at all and the counters moved 1/1 and 2/2. Background
traffic on this node moves both counters equally. Across the four collection rows
that leaves eight readings against five unknowns, one shared S and a drift per
row, and S comes out 3 every time.

**The cause was a five-second sleep, not a slow ingest.** An earlier version of
this section said the run's ingest "took about 25 seconds". That was invented and
the harness timestamps exclude it: that pass ran four ingests, sixteen
`/debugstore` reads and four connections inside 122 seconds, of which 100 seconds
were its own sleeps. The ingest took **0.11 seconds**, the same as the three runs
that came out exact.

What differed is the **measurement window**, the span between the two
`/debugstore` reads that bracket the ingest. The first harness slept five seconds
inside that span before the second read; the second read immediately. Every chunk
anything else on the node stored during that sleep is counted as though the ingest
had stored it.

**The size of the excess is consistent with that, and an earlier version of this
paragraph fitted it rather than checking it.** This node's three 20-second control
windows read 13, 7 and 0, so between about 0.35 and 0.65 chunks a second, and an
excess of six needs somewhere between nine and eighteen seconds of window at
those rates. **The window's length was never recorded**, so the product cannot be
checked at all. The earlier version quoted the highest of the three rates against
an unrecorded nine-second window, which made the arithmetic land exactly on the
six it was explaining. That is the seventh instance of the pattern this document
names above and it is withdrawn. The claim that needs no arithmetic is the one
carrying the point: the sleep is the only difference inside the window, and
removing it gave 303 against 303 three times.

**Both ends of the window are fuzzy by the cost of reading them.** `/debugstore`
walks a retrieval index of about 2.5 million entries, so each bracketing read
takes time that is itself inside the window and was not measured. That is a
further reason exactness here is a property of the harness rather than of the
node.

Two consequences for the spec, and both are now in it. **Exact equality is a
property of a tight window rather than of a quiet node**, so demanding it without
requiring the tight window would reject a correct node measured by a looser
harness, which is what the first correction in this change did. And **a flat
control window does not prove a flat measurement window**: this run's control
read zero and its measurement drifted by six, so drift here arrives in bursts. A
single differing run is therefore repeated with a tighter window rather than
treated as a result.

### 6. An over-limit collection

**The first pass at this arm measured nothing, and a review caught it.** It
reported "507 leaving `TotalChunks` exactly flat" as evidence of no residue. But
its run 1 ingested the 20 MB archive **successfully**, and the runs after it
re-posted an archive whose chunks the node therefore already held. `TotalChunks`
cannot rise for a chunk already stored, so it reads flat on a leak exactly as it
does on a clean refusal. The observable had no power at all. Those rows are kept
at the end of this section for the record; they are not the result.

Re-run with a **never-ingested archive of fresh random bytes for every attempt**,
and with `ReferenceCount` read as well, because it rises for an already-held
chunk where `TotalChunks` cannot, so it detects a re-Put that `TotalChunks`
hides.

Two passes, **eight refusal runs**. The second is the better harness, because it
reads the control window over a span matched to the measurement window instead of
a fixed twenty seconds, which is the distinction the arm 5 section turns on. Both
are reported; dropping the first pass silently would be the selection this
document refuses elsewhere.

First pass, `t19b-arm6-rerun.txt`, rows `run1` to `run3`:

| Run | Control drift, both counters | HTTP | `TotalChunks` rise | `ReferenceCount` rise | `held` |
|---|---|---|---|---|---|
| 1 | **+13** | 507 | +1 | +1 | 126,060 |
| 2 | +7 | 507 | 0 | 0 | 126,060 |
| 3 | 0 | 507 | 0 | 0 | 126,060 |

Second pass, `t19c-arm56-drift.txt`, rows `arm6-refusal1` to `arm6-refusal5`:

| Run | Control drift, both counters | HTTP | `TotalChunks` rise | `ReferenceCount` rise | `held` |
|---|---|---|---|---|---|
| 1 | 0 | 507 | 0 | 0 | 127,878 |
| 2 | 0 | 507 | 0 | 0 | 127,878 |
| 3 | +1 | 507 | +2 | +2 | 127,878 |
| 4 | 0 | 507 | 0 | 0 | 127,878 |
| 5 | 0 | 507 | 0 | 0 | 127,878 |

**Five of the eight qualify fully**, with an exactly flat control window and an
exactly zero rise on both counters, which is the arm passing with two runs to
spare. Six of the eight have a zero rise; the sixth, first-pass run 2, has a
control window of +7, so its rise is zero but its window is not flat.

**First-pass run 1 is the strongest single reading in the set** and is easy to
misread as the weakest. Its rise of **+1** sits against a control window of
**+13**, so the refusal moved the counters far less than the node moved them when
left alone. **No ratio is claimed.** The +13 is counted over a fixed twenty-second
control window and the +1 over a measurement window that was never timed, so the
spans are not equal and dividing one by the other would treat them as though they
were. An earlier version said "thirteen times less".

**Second-pass run 3 is invalidated, not excused.** Its control window is +1, and
the spec's own clause discards a run whose paired control window is not flat. It
is discarded under that clause rather than argued away, which is the point in a
document whose case is that a written rule beats a judgement made afterwards.

**`held` is unchanged within each pass, and it is a narrower check than it
looks.** It reads 126,060 through the first pass and 127,878 through the second,
across **eight refusals and the five timing posts** the second harness makes
before its measured ones, thirteen readings in all. But `committed`, which `held`
reports, is raised in exactly one place, `s.committed += chunks` inside `commit`
(`pkg/storer/localingest.go:135-145`, the increment at `:143`), and `commit` has
a single call site, `:247` inside `Done`. A refusal moves `reserved`, a separate
field, and the limit check at `:116` reads `committed + reserved + n`. So a
refusal **cannot** move `held` however much it leaks. What the reading
establishes is that `Done` did not run. That is worth having and it is **not**
comparable to the chunk counters, which an earlier version of this section
implied by calling it the counted half of a pair. The residue question is settled
by `TotalChunks` and `ReferenceCount`, because those are arrived at independently
of anything the code under test reports.

**A zero rise here is a stronger result than a zero rise usually is.** With
`held` at 127,878 against a limit of 131,072, a refused run admits and stores
about **3,194 chunks** before the limit binds, and the cleanup path then removes
them. So both counters reading exactly 0 across the bracketing reads is evidence
that a cleanup of roughly three thousand chunks completed, not that little
happened. It also sets the scale the sensitivity control has to beat, and shows
it is beaten comfortably: 303 is an order of magnitude below the leak the arm
exists to catch.

**The check has power, which is shown rather than assumed.** Three runs on the
same node minutes earlier ingested a small fresh archive that fits, and both
counters moved every time, by 303 and 300. Those are the rows labelled
`arm5-drift1` to `arm5-drift3` in `t19c-arm56-drift.txt`. The row labelled
`sensitivity` in `t19b-arm6-rerun.txt` is **not** one of them: that is the
five-second-window run discussed under arm 5, and the labels crossing between the
two files is worth stating so a reader following the citation is not misled. A
flat reading is evidence of no residue only if a real ingest would not have read
flat too. These say it would not.

The 507 body carries both figures: `{"message":"local ingest limit reached",
"held":127878,"limit":131072}`.

#### The first pass's rows, kept for the record

| Run | Control drift | HTTP | `TotalChunks` rise after | Note |
|---|---|---|---|---|
| 1 | 0 | **201** | 5,832 | it fit, and this is the run that made the archive already-held |
| 2 | 0 | 507 | +2 | |
| 3 | 22 | 507 | 0 | control not flat |
| 4 | 0 | 507 | 0 | |
| 5 | 0 | 507 | 0 | |
| 6 | 1 | 507 | 0 | control not flat |
| 7 | 0 | 507 | 0 | |

**Run 1 is reported rather than dropped, and it matters twice.** A 20 MB archive
cost 5,832 chunks against 11,228 of headroom, so it was accepted and tested a
large ingest instead of the limit. Sizing an archive to cross a limit needs the
headroom read first,
and that run is what then gave arm 6 a limit to bind against.

**No conclusion is drawn from this pass.** An earlier version of this document
argued that run 2's `+2` against a flat control window "reads as background
rather than residue", and counted it as one of three qualifying runs. That is
precisely the smoothing this document refuses three sections earlier for arm 4b,
and it was applied here without noticing. Under the spec's own rule, a `+2` rise
against a flat control window is a failing run, and the rule says a single
failing run is a reject rather than an average. The re-run above removes the
question, because it has four runs at exactly 0 against control windows at
exactly 0, so nothing has to be argued.

## The spec defects, every one found by running it

Four, not the two an earlier version of this document claimed. Three are in the
spec as merged and the fourth was written here while fixing the first. Two
further errors, in this document rather than in the spec, are recorded with the
defect they belong to.

Every one of the seven leaned the same way, towards a rule or a finding that
looked sharper than the evidence allowed. That is the useful generalisation, and
it is why each is kept here rather than silently repaired.

### One: arm 5 compared against the wrong counter, and would have rejected correct code

The merged spec makes Accept condition 5 "the reported chunk count equals the
`TotalChunks` rise", and rejects a difference in either direction beyond the
control drift. **On a collection that can never hold.** Every unencrypted
manifest shares a small number of canonical node chunks with every other one, so
a node that has ingested anything before already holds them, and `TotalChunks`
does not rise for a chunk that is already stored. The shortfall measured 3 on
every collection between 26 and 303 chunks.

`ReferenceCount` is the invariant the arm was reaching for: it rose by exactly
the reported count in all six direct measurements, collections and blob alike.
The spec is corrected to compare against it, with the `TotalChunks` rise and
`SharedSlots` kept as the evidence that explains any difference.

The spec's reject clause also said a run whose `SharedSlots` or `ReferenceCount`
moved should be discarded as content the node already held. `ReferenceCount`
moves on **every** ingest, by one per chunk stored, so that clause would have
discarded every run ever made.

### Two: the first correction to arm 5 was wrong in three ways

The replacement rule demanded the reported count equal the `ReferenceCount` rise
**exactly**, controlled the window on `TotalChunks`, and kept an invalidation
clause referring to "the constant a collection always shares". All three are
wrong, and a review found them before they could reject a correct node.

- **Exactness was demanded without requiring what makes it hold.** One run gave
  reported 303 against a `ReferenceCount` rise of 309, on a node this document
  calls correct, and under the rule as first corrected that run had fresh content
  and a flat control window, so it qualified, it differed, and it was a **reject**.
  The rule now requires the tight measurement window that makes exactness true,
  and treats a single differing run as one to repeat rather than as a result.
- **The control window was on the wrong counter.** `ReferenceCount` is
  database-wide and rises for a chunk the node already holds, which `TotalChunks`
  cannot. So a window flat on `TotalChunks` is no evidence that `ReferenceCount`
  is quiet, and the rule decided on one counter while controlling the other.
- **There is no such constant.** 3 was measured on every collection from 26 to
  303 chunks. A clause naming a constant without writing its value down cannot be
  applied at all, and it was also unnecessary: deduplication does not break the
  `ReferenceCount` comparison, since a `Put` of an already-held address still
  raises that chunk's reference count and the session still counts the address
  once. Only a repeated address **within one archive** breaks equality, and the
  spec now forbids that directly, which is a rule the original measurement
  satisfied by luck in using random bytes.

**A third error, found in the same review and made while fixing the second.**
This document claimed the rule had been corrected "back to equality within the
observed drift" when the spec had not in fact been changed at all, and the ledger
row repeated the claim. Applying the spec as it actually stood to the 309 run
still produced a reject. Announcing a correction is not making one, and the only
reason it was caught is that the reviewer applied the rule to the run rather than
reading the sentence that described it.

### Three: arm 1 destroys arm 4's precondition

Arm 1 requires a **stamped** upload of the same archive. The roots are identical,
which is arm 1's own result. So that upload publishes the content to the network
under the exact reference arm 4 needs to be unreachable.

The first attempt at arm 4 returned 200 for the root and the inner path in all
three runs, on content that was supposed to be reachable only by naming the
holder. Nothing was wrong with the node. Rerun on an archive that was ingested
and never stamped, it returns 404 in all three runs.

The spec now says arms 4 and 4b take an archive that has never been stamped, and
the invalidation list says that reusing arm 1's archive invalidates them. The
list also gained the connectivity gate arm 4b's second pass ran under, which had
been applied as an after-the-fact judgement rather than a rule.

### Four: arm 6's residue check had no power against a reused archive

The merged spec asks arm 6 to show an over-limit ingest leaves no residue, and
measures that on `TotalChunks`. It never says the archive must be one the node has
not already ingested, and without that the observable is empty: an already-held
archive contributes no new chunk addresses, so the counter reads flat whether the
refusal leaked or not. The first pass at the arm ingested its archive
successfully on run 1 and then measured refusals of what may have been the same
archive.

The spec now requires a never-ingested archive per run, `ReferenceCount` beside
`TotalChunks`, and a **sensitivity control**: a small fresh archive that fits and
must move both counters. The general form of the rule is worth keeping in mind
beyond this arm: a null result is evidence only when a positive result would have
looked different, and nothing in the first pass established that.

## Two harness faults worth recording

Both were in the measuring script rather than the node, both produced plausible
output rather than an obvious failure, and the second one hid a real result for
two passes.

**One output file for every path, and no size printed.** A first pass at arm 4b
wrote every fetch to the same file, so `/blob.bin` reported the SHA of the
previously fetched file and looked exactly like a manifest serving the wrong
content for a path. Fetched on its own it is 16,384 bytes with the correct SHA,
and with one output file per path eleven of that pass's twelve fetches were
correct, the twelfth being the bare root's 404 in run 3, which is a real result
and not an artefact of the fault. An earlier version of this paragraph said all
twelve were correct, which contradicted the table two sections above it. Printing
the byte count alongside the hash is what would have caught the fault itself
immediately.

**Scoring on the HTTP status code.** The gated pass at arm 4b printed
`paths_200=4/4` for a run in which `/blob.bin` returned **zero bytes**, because
the script counted statuses. Rule 7 of `AGENTS.md` says in one line that an HTTP
200 from an API endpoint is never proof a node is healthy, and this is that rule
being broken inside the tool written to enforce it. The arm now scores a fetch
correct only on status, byte count and hash together, which is what turned a
reported pass into the reject above.

The general lesson is the same one both times: **a measuring script needs the
same adversarial reading as the code it measures.** A harness that cannot fail
the node cannot validate it either, and here one nearly published an accept for a
condition that was not met.

## What follows from arm 4b

The implementation is not changed and #340 is not reopened. Every arm that tests
#340's own code passes, and the arm that does not fails on the retrieval path,
which this change does not touch: the collection's single-chunk files travel the
same hinted path through the same manifest and never failed.

What arm 4b establishes is narrower and worth saying plainly: **serving a
multi-chunk file from a node that holds it, to a node that names it as the
holder, is not yet reliable.** For hosting a website that is not a detail, since
any image or stylesheet above 4,096 bytes is multi-chunk. The feature is usable
for a holder serving itself, which arms 2 and 3 show without a failure, and
provisional for a remote reader.

The evidence belongs on
[#313](https://github.com/crtahlin/wasp/issues/313) rather than in a new issue,
because #313 is the same observation and is open. What this pass adds to it is
the contrast the earlier reports did not have: **single-chunk content on the same
path, to the same peer, in the same collection, at the same moment, never
failed.** That narrows the candidate causes to ones that need more than one
chunk, which is what #343's reading of `joiner.ReadAt` predicts and what a
connectivity or discovery fault would not.

It also gives #313 a **cheap reproduction**, which every arm on that issue so far
has lacked. A collection holding one four-chunk file fails in roughly one attempt
in six at sub-second latency, and a single-chunk file in the same collection is a
built-in control saying whether a candidate fix broke the path rather than
removed the size dependence. Every previous arm on #313 needed a 4 MiB object and
a download lasting a minute or more, and none ever returned a complete body, so
this is also the first evidence that **small content over that path completes at
all.** Posted there as a comment rather than filed as a new issue.

## What this does not show

- **Nothing about a cross-host manifest root.** Both sides of arm 1 ran on one
  node. The entry content type for a tar comes from the host MIME table, so two
  nodes on different distributions could produce different roots for the same
  archive. That is the open question the spec records and neither claims nor
  tests.
- **Nothing about multipart.** Every arm used a tar. The two readers derive paths
  and content types differently, so a multipart collection is a separate case.
  The unit tests cover it; the bench does not.
- **Nothing about encrypted collections**, which cannot have a stable reference
  by construction and so have no equivalence to test.
- **Nothing about the bare-root intermittency's cause**, only its rate and a
  correlation with the provider not yet being connected.
- **Nothing about why a multi-chunk file returns an empty body**, only that it
  did, twice in thirteen attempts over the hinted path, and that the signature
  matches what #313 reports and #343 traced. No run here isolated a cause. The
  two failures and the eleven successes differ in timing and in nothing else the
  harness recorded, and thirteen attempts cannot separate timing from chance.
- **Nothing about where the size threshold for that failure is.** The fixture has
  exactly one multi-chunk file, at four chunks. Whether the rate rises with size,
  and whether a one-chunk file can fail at all, are unmeasured.
- **Nothing that makes a flat control window a guarantee.** One run had a control
  window reading zero and a measurement window that drifted by six, so drift on
  this node arrives in bursts and a quiet window does not predict the next one.
  Arm 5's exact equality holds because the measurement window was 0.11 seconds
  long, not because the node was quiet, and no run here establishes a drift rate
  that could be relied on as a tolerance.
- **Nothing about the arm 6 counters at a larger headroom.** Every refusal
  measured here admits about 3,194 chunks before the limit binds. A node with far
  more headroom would admit far more before refusing, and whether cleanup still
  returns both counters to exactly zero at that scale is untested.
- **Nothing about a directory large enough to cross a trie shape boundary.** The
  largest collection measured is 5,832 chunks and the sites are small.
- **Nothing about disk actually consumed**, as distinct from chunks counted. The
  usage figure counts distinct chunks and is an upper bound on new disk.

---

Generated with help of AI.
