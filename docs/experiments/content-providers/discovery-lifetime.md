# Giving provider discovery a lifetime it owns

Issue: [#369](https://github.com/crtahlin/wasp/issues/369).

Provider discovery and hinted connection are node-scoped background work, but
both derive their context from the HTTP request that happened to start them, so
both are cancelled when that request ends. On erasure-coded content the discovery
lookup is cancelled within milliseconds. This says what to change, what to
measure, and what the change is **not** expected to improve.

**This spec was rewritten three times, before any code, and every rewrite fixed a
defect in its own acceptance list rather than in the change.** The history is kept
because the pattern it records is more useful than anything the spec concludes.

- **Round one: three of six arms could not fail.** Two watched a log line that
  does not exist and one watched a connection that forms anyway. One cause for all
  three: `pkg/providers` has **no metrics and no success logging at all**, so there
  was nothing to observe and the arms were written around that absence instead of
  fixing it. The change now adds the counters.
- **Round two: a condition that could not be satisfied**, which is the opposite
  defect and was introduced while fixing the first. `LookupsCompleted` was defined
  so that a cache hit raised it too, making arm 5 demand a total the code cannot
  produce. A powerless arm passes whatever happens; an unsatisfiable one fails
  whatever happens. Both are acceptance-list defects and both are found the same
  way, by applying the condition to a row instead of reading it.
- **Round three, and then four: the same confound surviving two fixes.** The
  observable for "a dial completed" was `/peers`, which hive satisfies; then a
  counter, which the `ErrAlreadyConnected` short-circuit satisfies; then a counter
  split, which an underlay mismatch satisfies. Each fix went one level deeper into
  the same question and each was still satisfiable without a dial. What finally
  changed was the shape of the criterion rather than its subject, to an ordered
  conjunction, and even that is inference rather than proof: see the note under
  arms 2 and 6.

The lesson is not that reviews find things. It is that **every one of these was
found by applying a condition to a concrete row, and none by reading the sentence
that stated it.** A sibling experiment produced the same finding the day before,
which is why it is written here as a rule rather than an anecdote.

## Terms

- **Discovery** is the lookup that finds which nodes have announced a content
  key, followed by a connection to each. `Service.Discover`
  (`pkg/providers/providers.go:308`).
- **A dial** here always means one attempt to open a network connection to a
  peer, through `Options.Connect` (`pkg/node/providers.go:99`). Where this
  document means a configuration setting it says so in those words, because rule
  8 of `AGENTS.md` uses "dial" in that other sense and the collision is
  confusing.
- **The trigger** is the point at which a download starts discovery: the 64th
  `Get` through the download's getter wrapper. `discoverAfterChunks` is 64
  (`pkg/api/providers.go:35`), the condition is at `:115` and the `Discover` call
  at `:118`. The count is **shared across two wrappers**, the manifest loadsave
  at `pkg/api/bzz.go:546` and the data joiner at `:780,782`, because both hold
  the same `providerHint`.
- **A hint** is the `Wasp-Providers` request header naming provider overlays
  directly, which skips the lookup and goes straight to dialling
  (`Service.ConnectHints`, `pkg/providers/providers.go:338`).
- **The preferred set** is the per-content-key set of overlays a download asks
  first. `preferredCandidates` (`pkg/retrieval/preferred.go:184-202`) **filters
  it to connected full nodes**, at `:187` through `connectedFullNode`
  (`:207-213`), then keeps at most `maxPreferredAttempts`, which is 2 (`:34`). An
  overlay in the set that is not connected does nothing: it is dropped before the
  cap is applied.
- **A per-shard context** is the context a redundancy decoder creates for one
  prefetch fetch, `context.WithTimeout(ctx, g.config.FetchTimeout)` at
  `pkg/file/redundancy/getter/getter.go:130`, cancelled by its own `defer` as
  soon as that one fetch returns.
- **The reader context** is the request context, which lives for the whole
  download.
- **Node-scoped** means work whose value is not tied to the request that started
  it. Discovery is node-scoped: a provider, once connected, is useful to later
  downloads of that content and, for the reasons under Hypothesis, to none of the
  request that found it.

## Problem

`Discover` runs in a background goroutine whose context is derived from the
caller's:

```go
// pkg/providers/providers.go:308-313
func (s *Service) Discover(ctx context.Context, k []byte, set Adder) {
	s.goBackground(func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
```

The `AfterFunc(s.ctx, cancel)` on the third line is the shape this change needs
and shows the intent was already there: stop when the service closes. But
`context.WithCancel(ctx)` keeps the caller's cancellation too, and the caller is
a single chunk fetch.

`ConnectHints` (`:338`) carries the identical four lines at `:343-346`, and
therefore the identical defect, with the caller being the HTTP request itself.

### Measured, at both redundancy levels

Recorded in
[#369](https://github.com/crtahlin/wasp/issues/369#issuecomment-5739263845), raw
rows in `cp290/t18-discovery-cancel.txt`. One 16,777,216-byte file of random
bytes, uploaded from the provider **twice with the same batch**, once at the
shipped default redundancy level and once at level NONE, so the two arms are the
same bytes differing only in redundancy. Both announced and network-held, so
neither is sole-source. The requester asked with `Swarm-Cache: false` and no
hint, so discovery was the only path that could supply a provider, and the
provider was not connected at the start.

| Arm | Runs | Download | Lookup cancelled | Dial cancelled |
|---|---|---|---|---|
| default level | 3 | 5.24 / 6.62 / 7.06 s | **3 of 3** | 0 of 3 |
| level NONE | 3 | 5.42 / 5.93 / 5.17 s | 0 of 3 | **3 of 3** |

**The issue as filed was too narrow, and the control arm shows it.** At level
NONE the 64th fetch is a reader fetch, so the lookup inherits the request
context, lives about five seconds and completes; the **dial** is then cancelled
when the response ends, every time. At the default level the 64th fetch is a
decoder prefetch fetch, so the lookup inherits a per-shard context that dies as
soon as that shard returns and never completes at all.

Either way the provider is unusable for the download that triggered it. Erasure
coding does not create the cancellation, it moves it from the end of the download
to the first few milliseconds. **Fixing only the context the trigger passes would
move the failure from the lookup to the dial rather than remove it**, which is
why the change is in `Discover` and not at the trigger.

### The cache never warms, so the defect renews itself

`Lookup` refuses to cache an interrupted result, deliberately:

```go
// pkg/providers/providers.go:278-282
records := s.verify(ctx, k, w, s.candidates(ctx, k, w))
if err := ctx.Err(); err != nil {
	// an interrupted lookup is not cached
	return records, err
}
```

That is correct on its own terms, and it means the `lookupCacheTTL` of ten
minutes (`:37`) never begins on erasure-coded content: every download repeats the
whole lookup and every one is cancelled. The cost is paid every time and the
benefit is never kept.

**This also contaminates the measurement, in the direction that flatters the
change**, and the Measurement section is built around that. Before the change
nothing is ever cached, so every before run performs a full lookup. After the
change a first run caches for ten minutes, so a second run of the same content
key satisfies "the lookup was not cancelled" from the cache rather than from the
fix. Any arm comparing before with after must therefore use a **fresh content key
per run**, and the one arm that wants to observe the cache must say so.

### The same defect in `ConnectHints`, with its own measured symptom

`ConnectHints` is called while preparing the request
(`pkg/api/providers.go:100-102`, gated on the header supplying overlays) with a
context derived from `r.Context()`, so a hinted dial is cancelled when that
request ends.

The [#340 measurement](directory-ingest-results.md) saw a symptom consistent with
this without naming it. Asking a holder for a never-stamped collection by hint,
the bare root returned **404 once in twelve tabulated attempts**, as the first
request in a sequence, and a second such 404 occurred in an earlier pass whose
rows were set aside for a harness fault. That second one carries the evidence,
because it ran with the provider read back as **not connected**, and it is
mentioned rather than counted for that reason. A 404 there is consistent with the
preferred set holding an overlay that is not yet connected, so
`preferredCandidates` filters it out and the request falls to peers that never
held the content.

**Two things an earlier draft of this section claimed and could not.** It gave the
dial "roughly 1.8 seconds" of lifetime, taking the figure from #340's **no-hint**
arm, where `ConnectHints` is never called and no dial exists; #340 records no
duration for a hinted request at all. And it said repeated requests "each make
partial progress", which is a claim about libp2p internals with nothing behind
it: each request starts a fresh `Connect`. Both are withdrawn. What remains is
that the dial cannot outlive its request, which is read from the code.

That symptom is **correlational** and is not the justification for the change on
its own. `ConnectHints` is included because the code defect is identical, the fix
is identical, and fixing one and not the other would leave the same four lines
wrong in the same file.

## What is not established, and must not be re-asserted

- **That this change makes any download faster.** It is predicted **not** to, and
  that prediction is part of acceptance.
- **That the 404s in the #340 arm were caused by an unfinished dial.** One
  correlation on one of two failures, one of which is not even counted.
- **That a discovered provider will be dialable.** A node can announce a key and
  be unreachable. Lookup completion and dial completion are measured separately.
- **That discovery is worth having.** This change makes discovery do what it was
  written to do. Whether that delivers anything to a later download is arm 3, and
  arm 3 may come back negative.
- **How long a dial to a provider takes.** Nothing measures it. The before data
  shows only that one was still running at about 3.5 seconds, which is
  unexplained on a two-node bench and is the single number that would justify the
  timeout chosen below.

## Hypothesis

Deriving discovery's context from the service rather than the caller makes the
lookup complete at every redundancy level and makes the dial survive the request,
so a discovered provider becomes a connected peer available to **later**
downloads of that content.

### The obvious observable is the wrong one

The obvious measure is whether the download that triggers discovery gets faster
or more complete. It will not, and using that as the primary observable would
produce a false negative for a change that worked.

The arithmetic says so before any run. A provider lookup takes a **median 1.67
seconds** when it finds something, range 1.61 to 1.67, and a median 1.54 when it
does not, range 1.52 to 1.56 (measured, [results.md](results.md), Table 3, whose
stated convention at `results.md:67` is median with the range in brackets, not
mean). At level NONE, where the lookup already completes, the dial was **still**
cancelled at the end of the response in all three runs, having had about 3.5
seconds. Chunk 64 of a 4,096-chunk download arrives early and the download ends
about five seconds later; the lookup plus the dial does not fit in that.
**Both halves of that are asserted.** Nothing measures the time to chunk 64, and
the dial's duration rests on the 3.5-second figure this spec lists above as
unestablished and unexplained. The prediction is pre-registered on that basis and
the basis is weak; what it is not is a prediction made after the fact.

**Pre-registered prediction: the triggering download's wall time and delivered
bytes do not change.** If they do, that is a surprise to be explained before it
is claimed as this change working. This is the discipline
[#359](https://github.com/crtahlin/wasp/issues/359) was specified under and for
the same reason.

### What actually gets better

In decreasing confidence:

1. **The lookup completes at the default redundancy level.** A direct consequence
   with nothing else in the path.
2. **The dial completes and the provider becomes a connected peer.** Depends on
   the provider being dialable, which is not this change's to guarantee.
3. **A later download of the same content asks the provider first.** Where any
   value lives, and least certain: the peer must still be connected,
   `maxPreferredAttempts` is 2, and credit refusals apply to a preferred peer as
   to any other.

## Design

### The four lines

In both `Discover` and `ConnectHints`, in `pkg/providers/providers.go`:

```go
s.goBackground(func() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoverTimeout)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
```

`context.WithoutCancel` (Go 1.21; this module is on Go 1.26) returns a context
keeping the parent's **values** and dropping its cancellation and deadline.

- The caller's cancellation no longer ends the work.
- `AfterFunc(s.ctx, cancel)` still ends it when the service closes, which is the
  guarantee `goBackground` and `Close` already provide and which this must not
  weaken.
- `discoverTimeout` bounds it, so a hung lookup or dial cannot hold a goroutine
  for the life of the node.

**On the main path it keeps no values, and an earlier draft claimed otherwise.**
At the default redundancy level the 64th fetch is a decoder prefetch fetch, and
the prefetch context descends from `context.WithCancel(context.Background())` at
`pkg/file/redundancy/getter/getter.go:242`. It carries no server span and none of
the `getter.SetConfigInContext` values, which `pkg/api/bzz.go:549,764` apply after
`withProviders` has already run. So on that path `WithoutCancel` is equivalent to
`context.Background()`. It remains the right primitive because the level-NONE
path and `ConnectHints` **do** carry request values, but value propagation is not
a benefit on the path this issue is about.

### The counters, and why they are part of the change rather than the harness

`pkg/providers` has no `metrics.go`, no counters and no success logging. Every
`s.logger` call in the package is a failure or a debug detail
(`:317,328,354,358,369,376,466,475,538,634`), and the only one on the lookup path
is `:317` `"provider lookup failed"`, reached **only** from the `ctx.Err()`
branch. A cache hit returns at `:271-274` silently and a completed lookup at
`:302` silently.

So today a working lookup and a lookup that never ran are indistinguishable from
outside, which is why the first version of this spec contained arms whose null
result was identical to their positive result. The fix is to add the
observability, not to write arms around its absence. This follows the shape #299
argued for at `erasure-preferred.md:150-204` and for the same reason: no existing
counter can answer the question, so the change adds one.

**Identifiers are American, prose is left as the repository has it.** Metric
names and Go identifiers use `canceled` and `dialed`, per the style rules in
`AGENTS.md`, because a metric name is permanent surface that an operator's
dashboards depend on. Surrounding prose in this repository is predominantly
British and is not being normalised by this change. Stated so that the next
person does not normalise it in one direction at random.

New `pkg/providers/metrics.go`, house shape (exported fields, `newMetrics()`, a
`Metrics() []prometheus.Collector` method, as `pkg/retrieval/metrics.go:13` and `:162`),
namespace `bee`, subsystem `providers`:

| Field | Exported name | Incremented | Decides |
|---|---|---|---|
| `DiscoveriesStarted` | `bee_providers_discoveries_started` | on entry to `Discover`'s goroutine | arm 5's denominator |
| `HintedConnectsStarted` | `bee_providers_hinted_connects_started` | on entry to `ConnectHints`'s goroutine | arm 6's denominator |
| `LookupsServedFromCache` | `bee_providers_lookups_served_from_cache` | on the cache-hit return at `:273` | arms 1, 5 |
| `LookupsCompleted` | `bee_providers_lookups_completed` | **only** on the `:302` return | arms 1, 5 |
| `LookupsCanceled` | `bee_providers_lookups_canceled` | on the `ctx.Err()` branch at `:279-282` | arm 1 |
| `ConnectsDialed` | `bee_providers_connects_dialed` | the bee-level connect procedure ran to completion, handshake and topology included | arms 2, 6, and **not sufficient on its own** |
| `ConnectsAlreadyConnected` | `bee_providers_connects_already_connected` | the peer was already connected | arms 2, 6 |
| `ConnectsFailed` | `bee_providers_connects_failed` | the dial returned an error **other than** the run being cut off | reported per run |

**`ConnectsFailed` deliberately excludes cancellation**, which an earlier version
of this table did not say and the first implementation did not do. Neither loop
checked its context, so one shutdown or one expiry of the timeout called `Connect`
for every remaining record and counted up to `lookupCandidates` failures with no
dial attempted. That would have made this counter unusable for the one thing the
acceptance list reads it for, telling "the change works and providers are
undialable" from "the run was cut off". The loops now stop dialing once the
context is done, and add the found overlays to the preferred set first regardless,
because that set is worth keeping even when the dialing is cut off.

**`LookupsCompleted` must count only the `:302` return, and an earlier version of
this spec did not say so.** `Lookup` has three returns with no context error:
`:265` for a key that is not a plain reference, `:273` for a cache hit, and `:302`
for a lookup actually performed. Defining the counter as "returns with no context
error" makes a cache hit raise both it and `LookupsServedFromCache`, so arm 5
across three downloads would give +3 and +2 rather than the +1 and +2 its
condition demands. The condition would have been **unsatisfiable on correct
code**, and it was introduced while fixing the powerless arms. The two are
**opposite** classes of acceptance-list defect, which is worth keeping distinct: a
powerless arm passes whatever happens, an unsatisfiable one fails whatever
happens. Both are found the same way, by applying the condition to a row rather
than reading it.

**The dial counter must distinguish a dial from a peer that was already
connected, and this is what makes arms 2 and 6 possible at all.** The obvious
definition, "when `Options.Connect` returns nil", does not work, because that
closure returns nil on **two** paths (`pkg/node/providers.go:99-115`): a dial the
topology accepted at `:114`, and `errors.Is(err, p2p.ErrAlreadyConnected)` at
`:101-103`. On a two-node bench kademlia re-dials a peer in its address book, so
by the time `Discover` reaches `Connect` the peer is frequently connected
already, and a single counter would rise with no dial having happened. That is
the same confound as using `/peers`, moved inside a counter, and the arms would
pass without the change doing anything.

So the two nil returns are split in `pkg/node/providers.go`, which is why that
file is in the Files list. The `pkg/providers` call sites cannot tell the branches
apart from a bare nil, so the distinction has to be made where the branch is.

**The split narrows the confound and does not remove it, and a third version of
this spec claimed otherwise.** `ConnectsDialed` counts "the
`p2p.ErrAlreadyConnected` short-circuit did not fire", and that short-circuit is
**address-keyed rather than peer-keyed**. `s.peers.isConnected(info.ID,
remoteAddr)` (`pkg/p2p/libp2p/libp2p.go:1068`) requires both the peer ID in
`r.overlays` **and** a matching remote address in `r.connections[peerID]`
(`pkg/p2p/libp2p/peer.go:185-203`). An existing connection to the same peer on a
**different underlay** therefore does not take that branch: execution falls
through to `s.host.Connect` at `:1076-1078`, which returns nil when a connection
to that peer ID is already open, and the closure runs on to `return nil` at
`pkg/node/providers.go:114`. `ConnectsDialed` rises with no dial.

That is not a remote possibility on this bench. `Options.Address`
(`pkg/node/providers.go:76-92`) deliberately filters a record's underlays to
**public** ones and caps them at `MaxUnderlays`, which is 4, so the underlay set
in a provider record is liable **by construction** to differ from the address
kademlia dialled on.

The remedy is not a further counter. Arms 2 and 6 require an **ordered**
conjunction of three legs, set out in those arms: the overlay absent from `/peers`
when the response completes, the counter rising during the interval, and the
overlay's first appearance at or after the second the counter rose.

**This is inference and not proof, and the spec says so rather than leaving a
later reader to discover it.** `ConnectsDialed` means "the bee-level connect
procedure ran to completion". Ordering it against `/peers` makes a dial by
discovery much the most likely reading of an absent-then-present transition, and
it does not exclude every alternative: the counter still cannot distinguish a
dial that opened a connection from a connect procedure that completed over one
already open on another underlay. Settling it outright would need the
`ErrAlreadyConnected` determination made per peer rather than per address, which
is a change in `pkg/p2p/libp2p` and is not in this change's scope. What this arm
can claim is recorded on that basis.

The bench can also remove the mismatch rather than reason around it, and should:
read the provider's `/addresses` and confirm it advertises a **single** public
underlay, so a record's underlay set cannot differ from the address kademlia
dialled and `ErrAlreadyConnected` fires as the counter assumes. That is a
precondition below.

**Registration is not where an earlier version of this spec said it was.**
`providersService` is declared inside the `if o.ProvidersEnable` block at
`pkg/node/node.go:1589-1597`, and only `providersAPI` escapes it, typed
`api.Providers` (`pkg/api/providers.go:46-53`), which has no `Metrics()` method.
The registration block at `:1629-1636` is itself inside
`if o.APIAddr != ""`. So the implementation hoists the variable above the block
and registers the collectors only when providers are enabled.

Eight counters is permanent surface area, which rule 8's reasoning about
configuration applies to as well, so each row above names the arm it decides.
`docs/DIFFERENCES.md:166` shows that metrics get a row there, and these will.

### Why not pass the download's context instead

The trigger cannot tell which context it holds: a reader context on unencoded
content, a per-shard prefetch context on encoded content. A fix at the trigger
would have to take the request context from a context value, which is the same
coupling the defect is made of, and it would still leave the dial dying with the
response. The decision belongs where the lifetime is known, in the service.

### `discoverTimeout`, a constant and not a setting

Proposed **30 seconds**. Per rule 8 the order is measure first, expose second, so
this ships as a compiled-in constant rather than a configuration option.

**Its value is asserted, not measured, and that is stated rather than hidden.**
No run measures how long a dial to a provider takes; the only datum is a dial
still running at about 3.5 seconds, which this spec lists as unexplained.

**There is a better number to derive it from, and a consequence that is not a
risk but a behaviour.** The 15 second timeout at
`pkg/p2p/libp2p/libp2p.go:1076` is taken **per underlay**, inside the loop over
an address's underlays, not per overlay. A discovered provider's address carries
up to `MaxUnderlays`, which is 4 (`pkg/providers/keys.go:31`), so a **single**
`Connect` can consume up to 60 seconds, more than the whole bound. `ConnectHints`
then dials up to `maxProviderHints`, which is 8, serially
(`providers.go:348-360`).

So the bound admits **at most about two overlays, and fewer where an address
carries several underlays**, where it can cut off inside the first overlay's
underlay list. An earlier version of this paragraph said "two overlays" flatly,
reading the 15 seconds as per overlay; that holds only where every address has
exactly one underlay. The bench precondition below, that the provider advertises
a single public underlay, is what makes the two-overlay figure true **on the
bench specifically**, which is the only place the arithmetic is load-bearing.

That is a consequence of the constant rather than a possibility, it is not in the
risk list because it is certain, and 15 seconds times the underlays worth trying
is the quantity the constant should be derived from.

What would justify making it configurable later: a measured dial that needs longer
on a slow or NAT-bound peer, or measured goroutine accumulation. Neither exists.

It must be reachable from a test. `Service.now` (`:124`) is injectable but
`context.WithTimeout` reads the real monotonic clock and ignores it, so the
constant is exposed through `export_test.go` for the timeout test below.

**One case where the change shortens rather than extends the work's life.** On a
download lasting longer than 30 seconds, discovery today inherits a context that
outlives the timeout. After the change it is bounded at 30 seconds. The change is
not purely widening and the timeout must be chosen with that in mind.

### Relationship to #299, which this does not collide with

[#299](https://github.com/crtahlin/wasp/issues/299) concerns the same prefetch
context losing the **preferred set**, and #369 says the two should be settled
together so the fix is not made twice. They can be, and the reason is not a file
list.

#299's Design (`erasure-preferred.md:290-307`) argues that `Discover` is
unaffected by its change because **`Discover` reads through a different getter
entirely**, the plain storer one wired at `pkg/node/providers.go:58`
(`Getter: localStore.Download(false)`), not the download's wrapped getter. That
reasoning is what discharges the overlap, and it survives this change untouched,
because this change alters only when `Discover`'s goroutine stops.

The file lists are disjoint as well, which is a check rather than the argument:

| | #369, this change | #299 |
|---|---|---|
| Files | `pkg/providers/providers.go`, new `pkg/providers/metrics.go`, `pkg/node/node.go` for registration | `pkg/retrieval/preferred.go`, `pkg/retrieval/retrieval.go`, `pkg/retrieval/metrics.go`, `pkg/api/providers.go` |
| Function | `Discover`, `ConnectHints` | `providerGetter`, `HasPreferredPeers` |

An earlier draft of this table named a single #299 file and was wrong; #299 lists
four (`erasure-preferred.md:622-651`), including the
`PreferredCandidatesSelected` counter that spec spends `:150-204` establishing as
non-optional. None of the four is `pkg/providers/providers.go`.

This change deliberately leaves the trigger at `pkg/api/providers.go:118`
untouched, including its `retrieval.WithPreferredPeers(ctx, nil)`, which is
correct and must stay: the lookup's own reads must not go to the download's
preferred peers.

**So the gate #299 records is satisfied by this spec merging, not by this code
landing first.** Either order works.

## Configuration

None. See `discoverTimeout` above for what would justify a setting later.

## What this risks

- **`ConnectHints` is the unbounded path, and an earlier draft analysed the wrong
  one.** `Discover` fires at most once per download: the atomic at
  `pkg/api/providers.go:115` equals 64 exactly once, so its count is bounded by
  concurrent downloads. `ConnectHints` fires from `withProviders` on **every**
  request carrying the header (`:100-102`), with no gate and no cache, and dials
  up to `maxProviderHints` = 8 overlays **serially** (`providers.go:348-360`)
  with no per-dial timeout of its own. A page issuing many hinted requests today
  creates goroutines the request kills; after the change each lives up to 30
  seconds. That is the real exposure.
- **The lookup cache does not bound `Discover` either, and the draft said it
  did.** `goBackground` is called at `:309` **before** `Lookup` consults the
  cache, so a cache hit shortens the goroutine's life without preventing it. And
  one discovery is not one goroutine: `candidates` spawns `Slots` = 8
  (`:489-500`, `keys.go:27`) and `verify` one per owner up to `lookupCandidates`
  = 16 (`:519-544`, `:35`), so the fan-out is about seventeen, not one.
  `goBackground` (`:198`) bounds none of it. Recorded as a known unbounded edge
  rather than fixed here, because bounding it is a separate change with its own
  measurement.
- **`Close` may now wait on work a finished request would have cancelled.**
  Bounded by how fast `Options.Connect` notices cancellation, because
  `AfterFunc(s.ctx, cancel)` already exists at `:312` and `:345` and is
  unchanged, so `Close` cancels `s.ctx` and the work stops. It is **not** bounded
  by `discoverTimeout`, and an earlier draft said in bold that it was while the
  next sentence said the opposite.
- **Connections the node did not need.** A dial completing after the request has
  gone leaves a peer connected that nothing is using, consuming a connection
  slot. A provider connection is what the feature is for, so the cost is
  intended, and it is **not mitigated**: an earlier version of this spec said
  existing peer management drops idle connections, which is not cited and is not
  obviously true, since kademlia holds peers in bins rather than pruning on
  idleness. Withdrawn rather than left standing, because it was the whole
  mitigation. **The cost is operator-visible, which is why
  `docs/DIFFERENCES.md` gains a row**, see Upstream portability.
- **A cached empty result now reaches the operator API.** With the lookup
  completing, an empty result is cached for ten minutes where today the
  cancellation caches nothing. `s.cache` is shared with
  `GET /wasp/providers/{reference}/lookup` (`pkg/api/providers.go:353`), so a
  background discovery that finds nothing silently answers an operator's explicit
  lookup of the same reference from that empty cache for up to ten minutes. That
  is `lookupCacheTTL` behaving as designed and newly reachable, and it is the
  risk here most likely to surprise someone.
- **A span whose parent has ended.** `pkg/api/api.go:497-498` starts the server
  span with `defer span.End()`. On the level-NONE path and for `ConnectHints`,
  `WithoutCancel` keeps that span, so spans started after the response become
  children of an ended span.

## Protocol impact

None. No wire format, no constant in `.github/protocol-freeze.lock`, no message
type, no timing a stock peer observes. The change is local to when a goroutine
stops, plus counters. `make protocol-freeze` is still run.

## Measurement

Bench, `bench-1` as provider and `bench-2` as requester.

**Before and after arms run in the same session where the observable is wall
time.** Rule 7 requires node state to be matched, and
`erasure-preferred.md:496-500` records a result this repository had to withdraw
for exactly that reason. That applies to arm 4 without exception, and to arm 3.

**It cannot apply to arm 1, and finding out why cost three attempts.** Alternating
builds needs a restart per run, and **a restart of the requester is not cheap on
this bench**: the node comes back with `localstore sharky .DIRTY file exists:
starting recovery` and rebuilds before `/readiness` reports ready or a single peer
reconnects. Measured on a store of about 2.5 million chunks: still `notReady` with
**zero peers after five and a half minutes**, then ready with 79 peers about 47
seconds after the rebuild finished. So a restart costs roughly six minutes, and
six restarts in one run is about half an hour of recovery in a run that is otherwise
a few minutes of work.

**That is a cost, not a risk, and an earlier version of this paragraph said
otherwise.** These are experiment nodes, not nodes playing the redistribution
game, so a dirty store and a rebuild cost time and nothing else. Budget for it
rather than avoid it: arm 4 needs the alternating design and is worth the half
hour. Arm 1 does not, because its observable is structural rather than timing,
which is the only reason it is exempt.

So arm 1 takes its before state from the run recorded on
[#369](https://github.com/crtahlin/wasp/issues/369#issuecomment-5739263845) and
says so in the write-up. **The deviation is defensible only because arm 1's
observable is not wall time**: whether a lookup was cancelled is a structural
outcome of which context it inherited, not a timing measurement, and it does not
vary with how warm the node is. Arms 4 and 3 keep the same-session rule, which
means they cannot be run in the same pass as a build swap and need their own
design.

**Two things every arm here must wait for after any restart, and a first harness
waited for neither.** `/health` answers long before the node can serve anything:
it answered in four seconds while the node went on to spend minutes in recovery.
Wait for `/readiness` to report `ready` **and** for the peer count to come back
above a floor, and treat an HTTP 503 from a download as an invalid run rather
than as a result. A harness that waited only on `/health` produced ten 503s in
0.01 s across twelve downloads on both builds, and read the counters back empty
for the same reason.

### The per-run reset, which the arms cannot do without

Under the change, run 1 leaves the provider connected, which is the point of arm
2, so runs 2 and 3 would start connected and be invalidated by this spec's own
precondition. Disconnecting is necessary and **is not sufficient**: four kinds of
state survive it, and each one has produced or would produce a false pass.

Before every run of arms 1, 2, 5 and 6, and **not** between arm 2 and arm 3:

1. **Disconnect** the provider from the requester with `DELETE /peers/{address}`
   (`pkg/api/router.go:477-478`; the route variable is `{address}`), and confirm
   the overlay is absent from `/peers`.
2. **Re-read `/peers` at the moment the response completes.** Kademlia re-dials
   peers it holds in its address book, which the preconditions require it to
   hold, so the provider can be connected again by an unrelated mechanism partway
   through the run. A run in which it was already connected at that moment is
   invalidated, because `ConnectsAlreadyConnected` can then rise with no dial.
3. **Use a fresh content key**, a new upload of new random bytes, except in arm 5
   where the cache is the observable. A reused key is served from
   `lookupCacheTTL` for ten minutes and satisfies arms 1 and 2 without the fix
   doing anything.
4. **Confirm the provider actually wrote the current window**, with
   `GET /wasp/providers` on the provider (`providersListHandler`,
   `pkg/api/providers.go:303-331`), whose `windows` field comes from `a.Written`.
   That catches the most likely thing to be missing seconds after an upload and
   hands the harness the window number without computing it. It does **not** prove
   the record propagated to where the requester will look, which the guard below
   covers.

   **An earlier version of this step asked for `GET /chunks/{addr}` on the record
   and slot addresses, which no API returns.** Deriving them means reimplementing
   `pkg/providers/keys.go`: `RecordAddress` is
   `soc.CreateAddress(keccak256("wasp-providers-v1" || k || be64(w)), owner)`
   (`keys.go:44-56`) with `owner` the provider's `chainAddress` from `/addresses`,
   and the slot address needs `SlotFor` (`keys.go:64-66`) plus the index signer
   derived by reducing `keccak256("wasp-provider-index-v1" || k)` modulo the
   secp256k1 order (`keys.go:72-78`). That is keccak256, secp256k1 key derivation,
   Ethereum-address derivation and SOC address construction: a Go helper, not
   curl, and the step was written as though it were curl.

   **The guard that actually catches a lookup finding nothing is post-hoc, and it
   is already in acceptance 1**, which requires `ConnectsDialed` or
   `ConnectsAlreadyConnected` to rise. It is stronger than a pre-flight, which can
   pass and then have the chunk evicted before the run. It is therefore also an
   invalidation clause: a run where `LookupsCompleted` rose while all three
   `Connects` counters stayed flat found nothing, and is discarded rather than read
   as the lookup working.
5. **Reset accounting balances**, as `erasure-preferred.md:439-440` does and for
   the same reason: arm 3's observable increments only after `prepareCredit`
   succeeds, and arm 4 is wall time, so debt carried from run 1 suppresses one and
   moves the other.

**One thing the reset cannot clear, and it is node-wide rather than per-peer.**
A failed dial trips `connectionBreaker`, which is a **single breaker for the whole
node**, declared once at `pkg/p2p/libp2p/libp2p.go:490` and consulted at `:1092`.
So a failed dial to **any** peer can make the provider dial return
`p2p.NewConnectionBackoffError` and so `ConnectsFailed`, and a successful dial
anywhere resets it. A `ConnectsFailed` in a run therefore need have nothing to do
with the provider. `ConnectsFailed` is recorded per run so a monotone rise is
visible rather than read as a property of the change, and before and after
alternate. An earlier version of this note called the backoff per-peer.

Read back before every arm: both versions from `/health`, that the requester
holds none of the content, that `/blocklist` is empty, and `node/providers` set to
debug, which is `bm9kZS9wcm92aWRlcnM%3D` on `/loggers`, **padded** base64url.

**And read the postage batch from the provider rather than from a harness
configuration file.** A first pass used a batch identifier from a stored
configuration that the provider no longer had: every upload returned `batch with
id not found`, every reference came back empty, every announcement answered 404,
and all twelve downloads then answered 404 in 0.01 seconds. Twelve rows of
nothing. It was obvious only because the times were instant; a stale but existing
batch would have produced something that looked like a result. **Gate the run on
one small upload returning a real reference before anything long starts**, and
refuse to download unless every reference and every announcement came back good.
Uploads also fail transiently, one in twelve on a first pass, so retry an upload
before abandoning a run over it.

**And make no operator lookup on the requester for the duration of a run.** The
three `Lookups` counters are incremented in `Lookup`, which is also what
`GET /wasp/providers/{reference}/lookup` calls
(`pkg/api/providers.go:353`), so a single operator request during a run moves the
same counters the arms decide on: arm 5 demands exact totals and arm 1 invalidates
on `LookupsServedFromCache` rising. The counters are node-wide rather than scoped
to discovery, which is the same fact this spec already records about the shared
`s.cache`, and it is a precondition rather than a defect. `GET /chunks` is safe by
contrast: it calls `withProviders` with a nil key, so `providerGetter` returns the
bare getter and no discovery is triggered.

**And read the provider's `/addresses` to confirm it advertises a single public
underlay.** With more than one, a provider record's filtered underlay set can
differ from the address kademlia dialled, the `ErrAlreadyConnected` branch is
missed, and `ConnectsDialed` can rise with no dial, which is the confound arms 2
and 6 are built around. One underlay removes it at the source rather than
reasoning about it. An arm run against a multi-underlay provider is reported with
that noted, because the ordering requirement is then the only defence.

Three runs per condition minimum, reported with the spread. **Arm 3 runs once**,
and once is enough because it is reported and not required and its negative is
uninterpretable; a spread over an uninterpretable quantity would suggest more than
it can carry.

### Arm 1, the lookup completes at the default redundancy level

Three fresh 16 MiB uploads at the default level and three at level NONE, same
bytes per pair, announced and network-held. Requester asks with
`Swarm-Cache: false` and no hint. Alternating, disconnect before each.

Observables: `LookupsCanceled` and `LookupsCompleted`, plus
`LookupsServedFromCache` as a guard that no run was answered from cache.

Before, measured: 3 of 3 cancelled at the default level, 0 of 3 at level NONE.

### Arm 2, the dial completes and survives the request

Same runs as arm 1. **Three legs, all required**, because no one of them means a
dial completed after the request ended:

1. the provider's overlay is **absent** from the requester's `/peers` at the
   instant the response completes;
2. `ConnectsDialed` rises in the interval between that instant and the end of the
   poll, with `ConnectsAlreadyConnected` flat in that interval;
3. the overlay is **present** in `/peers` at the end of the poll.

Leg 3 was demoted to corroboration in an earlier version of this spec, correctly
as a **sole** criterion and wrongly as part of a conjunction: hive connecting the
two nodes does not satisfy leg 1, so the three together exclude it where leg 2
alone does not. Leg 2 alone is also insufficient for the reason under the
counters: it counts the connect procedure completing, not a dial.

**The read protocol matters, because a counter carries no timestamp, and the
three legs must be ordered rather than merely all true.** Poll **both `/peers` and
`/metrics` once a second** through the whole 45-second interval, which is longer
than `discoverTimeout` so that "never dialled" is distinguishable from "dialled
just after the poll stopped". Require that the overlay's **first appearance in
`/peers` is at or after the second in which `ConnectsDialed` rose.**

**Without that ordering the conjunction is still satisfiable with no dial by
discovery**, and a fourth version of this spec missed it. Kademlia re-dials the
provider from its address book, which the reset protocol below establishes it
does, and it can do so **inside** the interval rather than before it. Then leg 1
held, the overlay appeared, `Discover`'s `Connect` ran afterwards, missed the
`ErrAlreadyConnected` branch on the underlay mismatch described under the
counters, completed the handshake path and raised `ConnectsDialed`, and leg 3
held. All three legs, no dial. Ordering the first appearance against the counter
is what separates the two cases: if the overlay appears first, kademlia got there
and discovery only observed it.

> **Correction, added later: the ordering requirement above is withdrawn. It is
> not satisfiable by the behavior it exists to confirm.**
>
> The reasoning assumed that an overlay appearing in `/peers` before
> `ConnectsDialed` rises means something other than discovery connected the
> peer. The code says otherwise, and the order is fixed rather than incidental.
> `/peers` is `s.p2p.Peers()` (`pkg/api/peer.go:100-103`), reading the libp2p
> registry. On a discovery dial that registry entry is written by
> `addIfNotExists` (`pkg/p2p/libp2p/peer.go:141-162`) from `libp2p.go:1191`,
> **inside** `Connect` and before it returns. `ConnectsDialed.Inc()` runs in
> `countConnect` (`pkg/providers/providers.go:387-392`) **after**
> `s.opts.Connect` returns, and later still here because
> `pkg/node/providers.go:116` calls `kad.Connected` first.
>
> So a genuine discovery dial **always** writes the overlay before the counter
> rises. The requirement is therefore met only when the two land in the same
> sampling bucket, and fails whenever they straddle one. Real work separates
> them: a `FullClose` that waits on the remote (`libp2p.go:1201`), a statestore
> write (`:1211`), the `ConnectOut` notifier loop (`:1218-1226`) whose handlers
> send messages over streams, and `kad.Connected` reaching `Announce`. **How
> long that takes has not been measured here**, and the single observation
> available, run 1, separates the two by exactly one 0.2 second sample.
>
> This is why arm 2 run 1 was recorded as a failure in
> [discovery-lifetime-results.md](discovery-lifetime-results.md). Legs 1 and 2
> are recorded as holding in that run, and leg 3 follows from the overlay
> appearing. Only this requirement did not hold, and it could only ever have
> held by the two events landing in the same bucket, which is what happened in
> runs 2 and 3.
>
> **What this does not rescue.** The confound the requirement was written
> against is real: kademlia can re-dial inside the interval, all three legs then
> hold, and no dial by discovery occurred. Withdrawing the requirement leaves
> that case unexcluded, so **arm 2 is not settled by these runs in either
> direction**. Separating the two needs a pair of nodes that do not reconnect to
> each other on their own, which this bench cannot provide, as the results
> document concludes. Found while writing
> [#382](https://github.com/crtahlin/wasp/issues/382).

Reading the counters at two instants only, as an earlier version did, cannot
order anything and would also be satisfied by a dial that finished while the
request was still open.

`/peers` alone would not be a criterion, because hive and kademlia connect these
two nodes independently of discovery; that is read from the design rather than
measured. It earns its place only as legs 1 and 3 of the conjunction, where the
**transition** across the interval is what carries the meaning rather than the
end state.

Before: this arm cannot run before the change, because the counters do not exist.
The before state is the #369 observation that
`connect to provider failed ... context canceled` appeared 3 of 3 at level NONE.
The write-up must say that before is a log line and after is a counter, rather
than presenting a clean pair. **At the default level the before state has both
`Connects` observables flat**, because the lookup never completes and the loop at
`providers.go:319-331` never runs, so there is no before contrast at that level
at all.

### Arm 3, a later download asks the provider first, at level NONE only

After arm 2 has left the provider connected, a second download of the same
content with no hint. Observable: `retrieval_preferred_attempts`.

**Only step 5 of the per-run reset applies to this arm.** Balances are reset;
steps 1 to 4 are not applied, because step 1 disconnects the provider and this arm
needs it connected, which is the state arm 2 produced. An earlier version said the
reset was "not applied" between arms 2 and 3 and separately that balances were
reset before arm 3, which cannot both hold since balances are step 5 of that same
reset.

**Run at level NONE, and only there.** At the default level three independent
causes give a flat result and this arm separates none of them: the peer not being
connected, credit refusal (`results.md:109-121`, a window of about 58 chunks),
and the preferred set being lost on prefetch fetches, which is #299's entire
defect and is unfixed when #369 lands alone. At level NONE the set is not lost.

`preferred_attempts` is credit-capped, incrementing only after `prepareCredit`
succeeds (`pkg/retrieval/preferred.go:228-239`), which is the ground on which
#299 rejected it as a primary observable. It is used here as a **secondary**
observable for an arm that is reported and not required, and a negative is
**uninterpretable** rather than informative. Accounting balances are reset before
this arm for the same reason.

### Arm 4, the pre-registered negative

Wall time and delivered bytes from the same runs as arm 1, before against after,
in one session, with balances reset before each run. Predicts **no change**.

Decided on **non-overlapping three-run ranges**, as #299 does, not on a mean and
not on "within the before spread": the before spread at the default level is 5.24
to 7.06 seconds, a 35 per cent range that almost any result falls inside.

### Arm 5, the cache warms

Three downloads of the **same** default-level content within ten minutes, on a
requester restarted beforehand so the cache starts empty, with the per-run
disconnect applied and **`Swarm-Cache: false`** as in every other arm, so the
chunk cache cannot substitute for the lookup cache being measured.

Observables: `DiscoveriesStarted` rising three times, `LookupsCompleted` rising
**once**, `LookupsServedFromCache` rising **twice**. `DiscoveriesStarted` is here
as the denominator: without it, one completed lookup is indistinguishable from
one discovery having run at all.

Before: not measured, and it must be run as a before arm rather than asserted. An
earlier version stated "three lookups, three cancellations, nothing cached" as
the before state; the #369 measurement made one download per run and never three
inside ten minutes.

### Arm 6, the hinted dial survives its request

Provider disconnected from the requester and confirmed absent from `/peers`. The
requester makes **exactly one** hinted request for never-stamped content and then
no further request.

Observable: the same three-leg conjunction as arm 2, over the interval between
that request ending and the end of a 45-second poll: overlay absent from `/peers`
at the request's end, `ConnectsDialed` rising and `ConnectsAlreadyConnected` flat
in the interval, overlay present at the end of the poll.

This is the sharpest form of the arm, because the request is short and is not
repeated, so nothing can complete the dial by accident and the dial is certainly
still running when the request ends. **It is therefore the arm that actually
tests the lifetime claim**, together with arm 2 at level NONE.

Before: not measured, and must be run rather than asserted. The #369 measurement
was unhinted, 16 MiB, on network-held content; this is hinted, on a never-stamped
collection.

## Acceptance

Accepted when all of:

1. **(arm 1)** at the default level, in all three runs: `LookupsCanceled` does
   not rise, `LookupsCompleted` rises by exactly one, `LookupsServedFromCache`
   does not rise, and **the lookup found the provider**, shown by
   **any of the three `Connects` counters** rising, since a lookup that completes
   having found nothing would otherwise satisfy this. All three prove records were
   returned, which is the only thing this leg is for. An earlier version accepted
   only `ConnectsDialed` or `ConnectsAlreadyConnected`, which made arm 1
   **unsatisfiable on a bench whose provider is undialable**: the lookup would work
   perfectly, only `ConnectsFailed` would rise, no invalidation clause would fire,
   and the arm would fail. That contradicted this spec's own statement that
   provider dialability is not its to guarantee, and the deliberate demotion of
   that case from a reject to a reported outcome; and at level NONE
   `LookupsCanceled` stays flat **and `LookupsCompleted` rises by exactly one**,
   because a level-NONE run whose download never reached the 64-chunk trigger also
   leaves `LookupsCanceled` flat and an earlier version of this condition would
   have accepted it. Before at level NONE was 0 of 3 cancelled read from the log
   line and after is read from a counter, so that half is not a clean pair either;
2. **(arm 2)** all three legs hold in all three level-NONE runs: the overlay is
   absent from `/peers` when the response completes; `ConnectsDialed` rises
   during the 45-second interval with `ConnectsAlreadyConnected` flat in it; and
   the overlay's **first appearance** in `/peers` is at or after the second in
   which `ConnectsDialed` rose. **All three, in that order.** An earlier version
   of this clause restated only the middle leg, which put the structural fix in the
   arm and left it out of the list that gets applied later by someone who will not
   re-derive the arm;
3. **(arm 4)** the three-run wall-time ranges before and after **overlap**, and
   delivered bytes are unchanged. This is a **pass by not changing**;
4. **(arm 5)** across three downloads inside ten minutes,
   `LookupsCompleted` rises by exactly **one** and `LookupsServedFromCache` by
   exactly **two**, with `DiscoveriesStarted` rising three times;
5. **(arm 6)** the same three ordered legs hold in all three runs, over the
   interval between the single hinted request ending and the end of the 45-second
   poll;
6. **(unit)** for **both** the discovery and the hinted path: a caller context
   cancelled before the call does not stop the work; closing the service does
   stop it and `Close` returns within two seconds; the timeout abandons a dial
   that never returns; a run cut off before it dials calls `Connect` zero times
   while still keeping what the lookup found; both counter pairs distinguish the
   cases the arms assume; and **the started counters rise for a run that found
   nothing**, which is the property that makes them denominators rather than
   success counts.

**A faster non-overlap is neither accepted nor rejected: it is reported and
investigated.** Two versions of this spec have now left one of the three
dispositions of arm 4 undefined, first the slower direction and then the faster.
All three are now named. A faster result must be checked against the alternative
explanation before it is attributed to anything here: that
`ConnectsAlreadyConnected` rose and the provider served the download, which is not
this change working and is the likely cause given the arm 2 timing above.

**Arm 2 at the default redundancy level is deliberately not an accept condition,
and this is the arm's own limitation.** After the change the lookup completes at
about 1.7 seconds into a 5 to 7 second download, so the dial can finish while the
request is still running, and a counter that rises then proves the lookup works
and nothing about the dial outliving the request. The lifetime claim is only
tested where the dial is still running when the response ends, which is arm 2 at
level NONE and arm 6. The default-level runs are reported for arm 1's sake.

Arm 3 is **reported, not required**, and its negative is uninterpretable.

Rejected if any of:

- `LookupsCanceled` still rises at the default level in any run, which means the
  change did not reach the path;
- `Close` does not return within two seconds in the unit test, which means the
  shutdown guarantee was weakened;
- delivered bytes **fall** at any level;
- the three-run wall-time ranges do not overlap **in the slower direction**,
  which would mean the change costs the triggering download something. An overlap
  here reads as absence of harm rather than absence of information, for the reason
  `erasure-preferred.md:558-561` gives;
- a discovery goroutine outlives `discoverTimeout` plus one second, read from
  `/debug/pprof/goroutine?debug=2` filtered on `pkg/providers` frames. Without
  that observable named, this row could not have been checked.

**Not a reject, but an outcome to report:** `ConnectsDialed` staying flat while
`ConnectsFailed` rises in every run. That means the dial now runs to completion
and fails, so the change is correct and the feature still does not work. An
earlier version made this a reject clause, which contradicted this spec's own
statement that provider dialability is not this change's to guarantee.

A run is invalidated rather than counted if:

- the provider was connected to the requester at the start, or the disconnect was
  not confirmed against `/peers`;
- **(arms 2 and 6 only) the provider was already connected at the moment the
  response completed**, read from `/peers`, because leg 1 of those arms'
  conjunction then fails and `ConnectsAlreadyConnected` could rise without a dial.
  **Scoped to those two arms deliberately.** Unscoped, as an earlier version had
  it, this clause would invalidate arm 1's default-level runs whenever the dial
  finished inside the request, which is likely, since the lookup completes about
  1.7 seconds into a 5 to 7 second download; acceptance 1 needs three valid such
  runs, so its satisfiability would depend on the dial duration this spec says it
  does not know. It would take arm 4 with it, since they share runs, and arm 5,
  whose conditions involve no `Connects` counter at all. It would also contradict
  acceptance 1, which accepts `ConnectsAlreadyConnected` rising as evidence the
  lookup found records. An even earlier version exempted "the provider
  connecting", which exempted the one event that makes arms 2 and 6 vacuous;
- **a run where `LookupsCompleted` rose while all three `Connects` counters stayed
  flat**, which means the lookup completed and found nothing, so the arm is not
  testing what it claims;
- **the provider had not written the current window** before the run, read from
  `GET /wasp/providers`. An earlier version of this clause said "the announcement
  was not retrievable by the requester, see the reset protocol", pointing at a step
  that no longer exists: the retrievability guard is post-hoc and is the clause
  above;
- the content key was reused from an earlier run, outside arm 5;
- `LookupsServedFromCache` rose in an arm 1 or arm 2 run, which means the cache
  answered and the fix was not exercised;
- accounting balances were not reset before the run, for arms 3 and 4;
- `/blocklist` is non-empty;
- the two arms of arm 1 did not use the same bytes for a pair;
- the content was not network-held, so the download died before the 64-chunk
  trigger. That happened in the #369 work: a download of expired-batch content
  returned 404 in 5.5 seconds with **zero** `node/providers` lines, because it
  died at the manifest. It could be mistaken for a refutation and is not one;
- before and after ran in different sessions.

## Rollout and rollback

No migration, no on-disk change, no setting. Rollback is reverting the merge
commit. A node running this beside nodes that do not is unaffected, because
nothing crosses the wire.

## Upstream portability

Both functions are fork-authored, in `pkg/providers/`, which upstream does not
have: `git ls-tree upstream/v2.8.2 -- pkg/providers` is empty. So there is no
conflict surface at an upstream sync and **no `affects-upstream` label**. The
defect is ours, in our code, and rule 11 does not apply. `docs/UPSTREAM.md` gains
no row.

**`docs/DIFFERENCES.md` does gain a row.** An earlier draft argued it gained
nothing, which contradicted this spec's own risk bullet: the node now keeps
connections made for a request after that request has gone, and consumes a
connection slot for them. That is operator-visible, which is what rule 13 turns
on. The row records the lifetime change and the new counters, since an operator
choosing between releases would want both.

## Files and test plan

- `pkg/providers/providers.go`: four lines each in `Discover` and `ConnectHints`;
  `discoverTimeout` added to the constant block (`:30-54`) as the default for a
  per-service `timeout` field; counter increments; a context check before each
  dial in both loops.

  **The timeout must not be a package variable that a test writes**, which the
  first implementation made it. A test shortening a package variable races every
  other parallel test's background goroutines reading it, and that data race
  failed **every** test in the package under `-race`, including the ones that
  existed before. A per-service field removes the shared state rather than
  synchronizing it.
- `pkg/providers/metrics.go`, new: the eight counters in the house shape
  (`pkg/retrieval/metrics.go:13` and `:162`), subsystem `providers`, namespace
  `bee`.
- **`pkg/node/providers.go`: split the `Connect` closure's two nil returns**, so a
  connect that dialed is distinguishable from one short-circuited because the
  peer was already connected. This file was absent from an earlier version of
  this list, and without it the arms cannot work: see the counters section.

  **Carry the split in a `bool` return, not a sentinel error.** `Options.Connect`
  is `func(ctx, addr) (alreadyConnected bool, err error)`. A first implementation
  used a sentinel error for the already-connected case, which is a success
  signalled by a non-nil error: it inverts the language convention, and the next
  person writing the obvious `if err != nil { continue }` would silently turn a
  usable provider into a skipped one with nothing in the type system to stop
  them.
- `pkg/providers/export_test.go`: expose a **per-service** setter for the bound,
  not the package value, for the race reason given two entries above. An injected
  clock cannot reach `context.WithTimeout`, which is why a setter is needed at
  all.
- `pkg/node/node.go`: hoist `providersService` out of the `if o.ProvidersEnable`
  block at `:1589-1597` so the registration at `:1629-1636` can reach it, and
  register only when providers are enabled. `providersAPI` will not serve: it is
  typed `api.Providers` (`pkg/api/providers.go:46-53`), which has no `Metrics()`.
- `pkg/providers/lifetime_test.go`, `package providers_test`, rather than adding
  to `providers_test.go`: these tests share several stubs and read better beside
  each other. The shared service helpers stay in `providers_test.go`.
  - `TestDiscoverSurvivesCallerCancel`: a caller context cancelled **before**
    `Discover` is called, with a stub `Connect` recording calls, asserting the
    overlay reaches the set and the connect still happens. Cancelling before the
    call removes the race.
  - `TestDiscoverStopsOnClose`: a stub `Connect` blocking until its context is
    done, then `Close`, asserting `Close` returns within two seconds and the
    blocked call saw cancellation. **Needs a synchronisation point**: `Discover`
    reaches `goBackground`, which checks `s.closed` under `s.mu` (`:198-203`), so
    a `Close` racing the call makes the goroutine never start and the test pass
    vacuously. Signal from inside the stub that the goroutine is running first.
  - `TestConnectHintsSurvivesCallerCancel` and `TestConnectHintsStopsOnClose`:
    the same two properties for the hinted path, one test each.
  - `TestDiscoverBoundedByTimeout` and `TestConnectHintsBoundedByTimeout`: with
    the bound shortened through the per-service setter, a `Connect` that never
    returns is abandoned and `Close` still returns. **Both paths need this.**
    Leaving `ConnectHints` entirely unbounded passed the whole suite when only
    the `Discover` variant existed, and that is the path that fires on every
    request carrying the header with no cache in front of it.

    **The `Discover` variant needs a bound of seconds, not milliseconds**, and
    the hinted one does not. That bound has to cover the **lookup** as well as
    the dial, and the lookup fans out to `Slots` goroutines plus one per
    candidate; at 200 milliseconds under `-race` on one core it does not finish,
    no dial is attempted, and the test fails blaming the timeout for something
    the timeout did not do. Measured: three failures in five at
    `-race -count=5 -cpu=1`. Both variants also wait for the dial to start
    before asserting, so that never reaching it reports as itself.
  - `TestLookupCountersDistinguishCacheFromWork`: a cache hit raises
    `LookupsServedFromCache` and **not** `LookupsCompleted`; a real lookup raises
    `LookupsCompleted` and not the other; a short key raises neither. This is the
    pair arm 5 decides on, and an earlier version of this spec defined the two so
    that a cache hit raised both, which made arm 5's condition unsatisfiable.
  - `TestLookupCanceledCounted`: a lookup on a cancelled context raises
    `LookupsCanceled` and not `LookupsCompleted`. This is arm 1's primary
    observable and drives its first reject clause, and it had no coverage at all
    in a first implementation.
  - `TestConnectCountersDistinguishDialFromAlreadyConnected`: the split above,
    which is what stops arms 2 and 6 passing without a dial. Its table also
    covers `context.Canceled` and `context.DeadlineExceeded` raising **no**
    counter, which is what stops one shutdown reading as many unreachable
    providers.
  - **The denominators need two tests, and the obvious one is not enough.**
    `TestDiscoveriesStartedIsADenominator` is arm 5's shape as a unit test:
    three discoveries of one key inside the cache window give
    `DiscoveriesStarted` 3 against `LookupsCompleted` 1 and
    `LookupsServedFromCache` 2, with the three calls **sequenced through the
    dial**, since concurrent runs race the cache and give three real lookups.

    That test alone does **not** hold the property, which was found by
    reproduction rather than by reading. Moving `DiscoveriesStarted` so that it
    counts only runs whose lookup returned records passes it, because every
    lookup in it finds records, while destroying the denominator outright and
    breaking acceptance condition 4. What catches that is
    `TestDiscoveriesStartedCountsAFruitlessRun`: a discovery of a key nobody
    announced, where the lookup runs, completes and finds nothing, and the
    counter must still say a discovery happened. That is the distinction the
    counter exists for, stated directly. Keep both.

    `TestHintedConnectsStartedCounted` is the same denominator for the hinted
    path, which has no cache in front of it, so counting once per call is the
    whole property.
  - `TestCancelledRunStopsDialingButKeepsTheSet` and
    `TestCancelledHintedRunDoesNotDial`: a run cut off before it dials calls
    `Connect` zero times, and the `Discover` one still adds every record it found
    to the preferred set. **Without these the guards are unobservable**, because
    a cancelled connect raises no counter, so either guard could be deleted and
    the whole suite would still pass.
- `docs/DIFFERENCES.md`: a row naming the exported counters and the lifetime
  change.
- `make format`, `make build`, `make test`, `make lint`, `make protocol-freeze`.

Fourteen test functions in all. The list above is kept level with the file
deliberately: this spec is the durable record, and two of these tests encode a
finding that is not obvious from reading them, that the cache-shaped denominator
test is insufficient on its own.

The unit tests cover the lifetime properties and that the counters mean what the
arms assume. Nothing in them shows the defect is gone on real content, which is
what the bench arms are for; the two are not substitutes.

---

Generated with help of AI.