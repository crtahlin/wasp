# Changelog

Changes this fork makes to upstream Bee. Upstream's own changes are absorbed as
single `chore(upstream)` entries — see `.upstream-base` for the release this
build derives from, and upstream's release notes for what it contains.

## [0.1.1](https://github.com/crtahlin/wasp/releases/tag/v0.1.1) - 2026-08-29


### Bug fixes
- make the store benchmarks run and reproduce ([#159](https://github.com/crtahlin/wasp/pull/159))
- benchmark against disk, not memory ([#161](https://github.com/crtahlin/wasp/pull/161))
- stop a stalled log consumer deadlocking the node ([#164](https://github.com/crtahlin/wasp/pull/164))
- bound the manage loop's wait for dials ([#165](https://github.com/crtahlin/wasp/pull/165))

### Documentation
- stop a stalled log sink deadlocking the node ([#157](https://github.com/crtahlin/wasp/pull/157))
- say what happens instead of project-management jargon ([#163](https://github.com/crtahlin/wasp/pull/163))
- tell the two missing-checks faults apart ([#166](https://github.com/crtahlin/wasp/pull/166))

### Miscellaneous
- drop a stray agent-worktree gitlink from the tree ([#168](https://github.com/crtahlin/wasp/pull/168))
## [0.1.0](https://github.com/crtahlin/wasp/releases/tag/v0.1.0) - 2026-08-27


### Bug fixes
- stop the generated changelog turning main red ([#44](https://github.com/crtahlin/wasp/pull/44))
- return 405 when the chain is disabled on available balance ([#47](https://github.com/crtahlin/wasp/pull/47))
- make the harness build on linux ([#50](https://github.com/crtahlin/wasp/pull/50))
- disable Green Tea GC, which corrupts the heap on Go 1.26 ([#70](https://github.com/crtahlin/wasp/pull/70))
- warn loudly when SIMD hashing is enabled ([#80](https://github.com/crtahlin/wasp/pull/80))
- make the shutdown wait configurable ([#82](https://github.com/crtahlin/wasp/pull/82))
- reset the dial breaker backoff after a successful call ([#83](https://github.com/crtahlin/wasp/pull/83))
- never let the dial breaker isolate a node completely ([#84](https://github.com/crtahlin/wasp/pull/84))
- readiness requires at least one connected peer ([#86](https://github.com/crtahlin/wasp/pull/86))
- give package tests a timeout that fits the race detector ([#97](https://github.com/crtahlin/wasp/pull/97))
- verify before opening bot-authored pull requests ([#95](https://github.com/crtahlin/wasp/pull/95))
- run the SIMD blob on a scratch stack, not the goroutine stack ([#94](https://github.com/crtahlin/wasp/pull/94))
- ask GitHub which files changed, not a shallow clone ([#102](https://github.com/crtahlin/wasp/pull/102))
- make the syso blobs rebuildable and reproducible ([#104](https://github.com/crtahlin/wasp/pull/104))
- stop the changelog dropping merges in silence ([#105](https://github.com/crtahlin/wasp/pull/105))
- clamp the final-block index in the XKCP wrappers ([#106](https://github.com/crtahlin/wasp/pull/106))
- raise the frame the generator emits for the 4-lane stub ([#107](https://github.com/crtahlin/wasp/pull/107))
- allow a read-only open ([#123](https://github.com/crtahlin/wasp/pull/123))
- give reserve-size-within-radius one definition ([#127](https://github.com/crtahlin/wasp/pull/127))
- stop gitignoring files that are tracked ([#126](https://github.com/crtahlin/wasp/pull/126))
- close the store without writing to it ([#139](https://github.com/crtahlin/wasp/pull/139))
- reconsider an RPC endpoint that was down at startup ([#140](https://github.com/crtahlin/wasp/pull/140))
- remove metrics that nothing writes to ([#147](https://github.com/crtahlin/wasp/pull/147))
- set the Debian section, which upstream leaves blank ([#152](https://github.com/crtahlin/wasp/pull/152))
- keep only merges that came via a pull request ([#153](https://github.com/crtahlin/wasp/pull/153))

### Documentation
- consistent status codes when the chain is disabled ([#46](https://github.com/crtahlin/wasp/pull/46))
- specify what the bench machines need ([#48](https://github.com/crtahlin/wasp/pull/48))
- record how to sample without stake, and a three-run rule ([#56](https://github.com/crtahlin/wasp/pull/56))
- concurrent reads in Sharky ([#57](https://github.com/crtahlin/wasp/pull/57))
- tuning constants become configuration, not edits ([#64](https://github.com/crtahlin/wasp/pull/64))
- require soaks to assert the node is under load ([#75](https://github.com/crtahlin/wasp/pull/75))
- separate I/O from hashing in sampler phase 2 ([#65](https://github.com/crtahlin/wasp/pull/65))
- record the three results the ledger was missing ([#101](https://github.com/crtahlin/wasp/pull/101))
- record the peer-discovery and shutdown fixes ([#103](https://github.com/crtahlin/wasp/pull/103))
- record the keccak work and correct a stale soak claim ([#110](https://github.com/crtahlin/wasp/pull/110))
- fallback blockchain RPC endpoints ([#111](https://github.com/crtahlin/wasp/pull/111))
- restore headroom between L0 compaction and write pause ([#112](https://github.com/crtahlin/wasp/pull/112))
- record the RPC failover measurement ([#124](https://github.com/crtahlin/wasp/pull/124))
- import the storage scaling bottleneck analysis ([#131](https://github.com/crtahlin/wasp/pull/131))
- sort sampler reads by physical position ([#132](https://github.com/crtahlin/wasp/pull/132))
- separate the expired-batch sweep from the reserve count ([#135](https://github.com/crtahlin/wasp/pull/135))
- backfill the ledger and the experiment tags ([#141](https://github.com/crtahlin/wasp/pull/141))
- decouple reserve doubling from receipt tolerance ([#143](https://github.com/crtahlin/wasp/pull/143))
- bounded evaluation of Pebble against goleveldb ([#144](https://github.com/crtahlin/wasp/pull/144))
- record the tag trap that misnames the fork version ([#151](https://github.com/crtahlin/wasp/pull/151))

### Features
- name experiment tags without a slash, and add export-patch ([#52](https://github.com/crtahlin/wasp/pull/52))
- rename the distribution to Wasp ([#53](https://github.com/crtahlin/wasp/pull/53))
- enable SIMD hashing where the CPU supports it ([#108](https://github.com/crtahlin/wasp/pull/108))
- expose level-0 depth and write-pause state ([#113](https://github.com/crtahlin/wasp/pull/113))
- add a failover backend for multiple RPC endpoints ([#117](https://github.com/crtahlin/wasp/pull/117))
- dial several blockchain RPC endpoints and fail over ([#118](https://github.com/crtahlin/wasp/pull/118))
- make the inbound chunk rate limits configurable ([#125](https://github.com/crtahlin/wasp/pull/125))
- make the recalculation and wake-up intervals configurable ([#130](https://github.com/crtahlin/wasp/pull/130))
- bound concurrent reserve lookups ([#134](https://github.com/crtahlin/wasp/pull/134))
- time the passes over the reserve index ([#137](https://github.com/crtahlin/wasp/pull/137))
- add pebblestore and measure it against goleveldb ([#145](https://github.com/crtahlin/wasp/pull/145))
- make the saturation limits configurable ([#148](https://github.com/crtahlin/wasp/pull/148))

### Miscellaneous
- tag defects that also exist in upstream Bee ([#72](https://github.com/crtahlin/wasp/pull/72))
- ignore the stray ./bee binary ([#96](https://github.com/crtahlin/wasp/pull/96))

### Performance
- drop ARM container images ([#51](https://github.com/crtahlin/wasp/pull/51))
- read without going through the shard actor ([#63](https://github.com/crtahlin/wasp/pull/63))
- separate chunk loading from hashing in the sampler ([#120](https://github.com/crtahlin/wasp/pull/120))
- ask batchstore once per batch, not once per chunk ([#121](https://github.com/crtahlin/wasp/pull/121))
- order sampler reads by physical position ([#133](https://github.com/crtahlin/wasp/pull/133))

### Reverted
- concurrent reads in Sharky crash a real node ([#66](https://github.com/crtahlin/wasp/pull/66))

### Testing
- cover the SIMD hasher under concurrency and contention ([#78](https://github.com/crtahlin/wasp/pull/78))
- fix the subscribe/publish race in the gsoc and pss ws tests ([#81](https://github.com/crtahlin/wasp/pull/81))
- wait for async events instead of assuming a fixed deadline ([#87](https://github.com/crtahlin/wasp/pull/87))
- verify the SIMD blob writes only inside its buffers ([#88](https://github.com/crtahlin/wasp/pull/88))
- bound cancellation waits by liveness, not latency ([#100](https://github.com/crtahlin/wasp/pull/100))
- measure level-0 depth against the compaction trigger ([#116](https://github.com/crtahlin/wasp/pull/116))
- signal instead of sleeping in the access-handler test ([#129](https://github.com/crtahlin/wasp/pull/129))

### Upstream
- sync ethersphere/bee v2.8.2 ([#149](https://github.com/crtahlin/wasp/pull/149))
## [0.1.0-test.1](https://github.com/crtahlin/wasp/releases/tag/v0.1.0-test.1) - 2026-08-21


### Bug fixes
- make the release workflow correct before first use ([#5](https://github.com/crtahlin/wasp/pull/5))
- create CHANGELOG.md on the first release ([#39](https://github.com/crtahlin/wasp/pull/39))
- cut releases through a pull request ([#40](https://github.com/crtahlin/wasp/pull/40))

### Documentation
- record the storage-layer briefing and require AI disclosure ([#6](https://github.com/crtahlin/wasp/pull/6))

### Features
- freeze the wire-protocol surface ([#2](https://github.com/crtahlin/wasp/pull/2))
- changelog, packaging and release workflows ([#3](https://github.com/crtahlin/wasp/pull/3))
- automate upstream absorption ([#4](https://github.com/crtahlin/wasp/pull/4))
- import the storage benchmark harness ([#42](https://github.com/crtahlin/wasp/pull/42))

### Miscellaneous
- establish fork process, disclaimers and version identity ([#1](https://github.com/crtahlin/wasp/pull/1))

### Performance
- scope CodeQL to Go changes and cache the build ([#41](https://github.com/crtahlin/wasp/pull/41))
