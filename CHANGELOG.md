# Changelog

Changes this fork makes to upstream Bee. Upstream's own changes are absorbed as
single `chore(upstream)` entries — see `.upstream-base` for the release this
build derives from, and upstream's release notes for what it contains.

## [0.1.5](https://github.com/crtahlin/wasp/releases/tag/v0.1.5) - 2026-09-26

### Bug fixes
- install declared dependencies before the deb upgrade check ([#480](https://github.com/crtahlin/wasp/pull/480))
- accept a port-only nat-addr and warn when a fixed one is stale ([#502](https://github.com/crtahlin/wasp/pull/502))
- stop on a bad config value instead of looping on it ([#492](https://github.com/crtahlin/wasp/pull/492))
- connect to a hinted provider before the download starts ([#506](https://github.com/crtahlin/wasp/pull/506))
- look up providers when a download cannot fetch its root chunk ([#509](https://github.com/crtahlin/wasp/pull/509))
- raise the provider wait bound to 20 s ([#513](https://github.com/crtahlin/wasp/pull/513))
- answer a peer behind NAT without waiting 10 s for addresses ([#516](https://github.com/crtahlin/wasp/pull/516))

### CI
- run the race detector once, in parallel shards, without fail-fast ([#519](https://github.com/crtahlin/wasp/pull/519)) ([#521](https://github.com/crtahlin/wasp/pull/521))

### Documentation
- record v0.1.4 as the release containing the changes ([#479](https://github.com/crtahlin/wasp/pull/479))
- spec release notes that say what the build does ([#482](https://github.com/crtahlin/wasp/pull/482))
- spec an always-on staking node running Pebble ([#486](https://github.com/crtahlin/wasp/pull/486))
- install with apt, not dpkg -i ([#488](https://github.com/crtahlin/wasp/pull/488))
- spec stopping on a bad config value instead of looping ([#491](https://github.com/crtahlin/wasp/pull/491))
- spec a nat-addr that follows the public IP ([#501](https://github.com/crtahlin/wasp/pull/501))
- spec connecting to a hinted provider before download ([#504](https://github.com/crtahlin/wasp/pull/504))
- spec looking up providers when the root chunk is missing ([#505](https://github.com/crtahlin/wasp/pull/505))
- ledger row for the port-only nat-addr ([#508](https://github.com/crtahlin/wasp/pull/508))
- ledger rows for the hinted connect and the lookup on a root miss ([#510](https://github.com/crtahlin/wasp/pull/510))
- raise the provider wait bound to 20 s after validation ([#512](https://github.com/crtahlin/wasp/pull/512))
- results for the port-only nat-addr, hinted connect and lookup on a miss ([#514](https://github.com/crtahlin/wasp/pull/514))
- spec answering a NAT'd peer without the 10 s address wait ([#515](https://github.com/crtahlin/wasp/pull/515))
- write down the two-node measurement and pre-push checks ([#517](https://github.com/crtahlin/wasp/pull/517))
- results for answering a NAT'd peer without the address wait ([#518](https://github.com/crtahlin/wasp/pull/518))
- spec cutting pull request CI to about 11 minutes ([#520](https://github.com/crtahlin/wasp/pull/520))
- re-check the comparison point before v0.1.5 ([#523](https://github.com/crtahlin/wasp/pull/523))

### Features
- publish notes that say what the build does ([#483](https://github.com/crtahlin/wasp/pull/483))

## [0.1.4](https://github.com/crtahlin/wasp/releases/tag/v0.1.4) - 2026-09-23

### Bug fixes
- stabilise the advertised underlay ([#225](https://github.com/crtahlin/wasp/pull/225))
- take the chain calls off the cheque path ([#301](https://github.com/crtahlin/wasp/pull/301))
- keep a preferred peer refused for credit ([#324](https://github.com/crtahlin/wasp/pull/324))
- give discovery a lifetime it owns ([#369](https://github.com/crtahlin/wasp/pull/369))
- carry the preferred set into erasure-coded downloads ([#299](https://github.com/crtahlin/wasp/pull/299))
- count a provider connect that needed no dial ([#382](https://github.com/crtahlin/wasp/pull/382))
- keep a provider on a chunk it alone holds ([#392](https://github.com/crtahlin/wasp/pull/392))
- close the store after the reserve worker stops ([#399](https://github.com/crtahlin/wasp/pull/399))
- do not wait for the network radius in retrieval ([#398](https://github.com/crtahlin/wasp/pull/398))
- pick the two test addresses in different buckets ([#410](https://github.com/crtahlin/wasp/pull/410))
- answer a malformed index-document header with 400 ([#415](https://github.com/crtahlin/wasp/pull/415))
- keep the error chain from the replica put ([#416](https://github.com/crtahlin/wasp/pull/416))
- reset the repayment counter when a peer reconnects ([#417](https://github.com/crtahlin/wasp/pull/417))
- do not prune a static peer ([#418](https://github.com/crtahlin/wasp/pull/418))
- answer a body that is not an archive with 400 ([#422](https://github.com/crtahlin/wasp/pull/422))
- serialize the cheque allocation per beneficiary ([#427](https://github.com/crtahlin/wasp/pull/427))
- stop evict and unreserve on the shutdown signal ([#431](https://github.com/crtahlin/wasp/pull/431))
- clamp the refresh allowance in settle ([#433](https://github.com/crtahlin/wasp/pull/433))
- keep the error budget while a provider answers ([#441](https://github.com/crtahlin/wasp/pull/441))
- answer 404 when the peer walk is depleted ([#448](https://github.com/crtahlin/wasp/pull/448))
- answer 404 when the peer walk is depleted, on pins too ([#452](https://github.com/crtahlin/wasp/pull/452))
- answer 400 for two malformed multipart uploads ([#454](https://github.com/crtahlin/wasp/pull/454))
- bound Close by the shutdown budget ([#458](https://github.com/crtahlin/wasp/pull/458))
- answer 400 for the last two malformed multipart bodies ([#466](https://github.com/crtahlin/wasp/pull/466))
- rebuild a chunk's preferred candidates when they run out ([#468](https://github.com/crtahlin/wasp/pull/468))
- lead the libp2p user agent with wasp ([#476](https://github.com/crtahlin/wasp/pull/476))

### CI
- guard the deb/rpm rename relationships ([#179](https://github.com/crtahlin/wasp/pull/179))
- verify the deb upgrade after a release ([#210](https://github.com/crtahlin/wasp/pull/210))

### Documentation
- method and verdict criteria for the Pebble A/B ([#190](https://github.com/crtahlin/wasp/pull/190))
- add an annotated configuration reference ([#191](https://github.com/crtahlin/wasp/pull/191))
- add schema descriptions and fix spec typos ([#192](https://github.com/crtahlin/wasp/pull/192))
- record the steady-state A/B results ([#193](https://github.com/crtahlin/wasp/pull/193))
- reserve-sample read and the L0 tuning that flips the verdict ([#194](https://github.com/crtahlin/wasp/pull/194))
- spec to pause pullsync during reserve sampling ([#200](https://github.com/crtahlin/wasp/pull/200))
- re-approach concurrent reads with a read/close guard ([#204](https://github.com/crtahlin/wasp/pull/204))
- fix the version-check example and changelog spacing ([#170](https://github.com/crtahlin/wasp/pull/170))
- record the #8 concurrent-reads re-measurement ([#212](https://github.com/crtahlin/wasp/pull/212))
- extend reserve doubling spec for #62, #219 ([#220](https://github.com/crtahlin/wasp/pull/220))
- spec for stabilising the advertised underlay ([#224](https://github.com/crtahlin/wasp/pull/224))
- reserve doubling results, sample is flat ([#226](https://github.com/crtahlin/wasp/pull/226))
- kademlia saturation does not gate fill ([#227](https://github.com/crtahlin/wasp/pull/227))
- state the cost of raising the sync rate limits ([#228](https://github.com/crtahlin/wasp/pull/228))
- analysis of address-derived placement, icebox ([#229](https://github.com/crtahlin/wasp/pull/229))
- the LevelDB block cache default does not need raising ([#12](https://github.com/crtahlin/wasp/pull/12))
- re-check the Pebble follow-ups ([#198](https://github.com/crtahlin/wasp/pull/198))
- correct the read-axis finding, engines at parity for #198 and #231 ([#233](https://github.com/crtahlin/wasp/pull/233))
- spec for sampler BMT hashing efficiency ([#236](https://github.com/crtahlin/wasp/pull/236))
- record the sampler BMT hashing result as neutral ([#236](https://github.com/crtahlin/wasp/pull/236))
- study a reserve-size-independent sample ([#235](https://github.com/crtahlin/wasp/pull/235))
- spec for probe-sample cost measurement ([#241](https://github.com/crtahlin/wasp/pull/241))
- probe-sample cost results ([#241](https://github.com/crtahlin/wasp/pull/241))
- estimate k for the probe-based reserve-size estimator ([#246](https://github.com/crtahlin/wasp/pull/246))
- ledger row for the probe-sample-k result ([#247](https://github.com/crtahlin/wasp/pull/247))
- methodology for client resource curves at radius events ([#252](https://github.com/crtahlin/wasp/pull/252))
- correct default index engine and pebble compaction default ([#250](https://github.com/crtahlin/wasp/pull/250))
- ledger row for the client resource-curves study ([#253](https://github.com/crtahlin/wasp/pull/253))
- add a tagline to the README ([#255](https://github.com/crtahlin/wasp/pull/255))
- spec for recovering stranded stake from historical contracts ([#257](https://github.com/crtahlin/wasp/pull/257))
- ledger row for stake recovery ([#261](https://github.com/crtahlin/wasp/pull/261))
- list every difference from the latest Bee release ([#263](https://github.com/crtahlin/wasp/pull/263))
- reflect the seeded stake-recovery catalog, default stays off ([#266](https://github.com/crtahlin/wasp/pull/266))
- add a table of Bee-origin issues (affects-upstream) ([#267](https://github.com/crtahlin/wasp/pull/267))
- rule 14, keep docs/UPSTREAM.md current ([#268](https://github.com/crtahlin/wasp/pull/268))
- the probe-based proof fails soundness ([#269](https://github.com/crtahlin/wasp/pull/269))
- ledger row for the probe-proof soundness study ([#270](https://github.com/crtahlin/wasp/pull/270))
- a sound sublinear proof of reserve size ([#271](https://github.com/crtahlin/wasp/pull/271))
- spec for reserve-proof-mode windowed proof ([#273](https://github.com/crtahlin/wasp/pull/273))
- spec for the client resource-curve harness ([#279](https://github.com/crtahlin/wasp/pull/279))
- spec for a configurable reserve capacity ([#283](https://github.com/crtahlin/wasp/pull/283))
- spec for exposing Pebble L0 read-amplification ([#286](https://github.com/crtahlin/wasp/pull/286))
- content-providers spec ([#290](https://github.com/crtahlin/wasp/pull/290))
- ledger row for content providers ([#294](https://github.com/crtahlin/wasp/pull/294))
- passive sharing analysis for content providers ([#290](https://github.com/crtahlin/wasp/pull/290))
- measurement method for content providers phase 1 ([#290](https://github.com/crtahlin/wasp/pull/290))
- spec for taking chain calls off the cheque path ([#301](https://github.com/crtahlin/wasp/pull/301))
- record the cheque acceptance work in the registers ([#301](https://github.com/crtahlin/wasp/pull/301))
- cheque acceptance cost results ([#301](https://github.com/crtahlin/wasp/pull/301))
- record the decision to keep the cheque change ([#301](https://github.com/crtahlin/wasp/pull/301))
- content providers phase 1 results ([#290](https://github.com/crtahlin/wasp/pull/290))
- spec the accounting gates ([#315](https://github.com/crtahlin/wasp/pull/315))
- ledger row for the accounting gates spec ([#318](https://github.com/crtahlin/wasp/pull/318))
- correct how the payment report moves the balance ([#319](https://github.com/crtahlin/wasp/pull/319))
- spec retrying a preferred peer refused credit ([#325](https://github.com/crtahlin/wasp/pull/325))
- extending provider credit per peer, not node-wide ([#322](https://github.com/crtahlin/wasp/pull/322))
- remove non-ASCII characters from fork-authored prose ([#323](https://github.com/crtahlin/wasp/pull/323))
- ledger row for the overdraft retry ([#324](https://github.com/crtahlin/wasp/pull/324))
- point what is planned at the issue tracker ([#334](https://github.com/crtahlin/wasp/pull/334))
- withdraw an uncontrolled comparison ([#324](https://github.com/crtahlin/wasp/pull/324))
- spec a per-peer provider payment threshold ([#327](https://github.com/crtahlin/wasp/pull/327))
- spec hosting content without a stamp ([#326](https://github.com/crtahlin/wasp/pull/326))
- measure hosting content without a stamp ([#326](https://github.com/crtahlin/wasp/pull/326))
- close three of the #326 measurement gaps ([#341](https://github.com/crtahlin/wasp/pull/341))
- sole-source retrieval at four file sizes ([#326](https://github.com/crtahlin/wasp/pull/326))
- diagnose what limits provider download rate ([#343](https://github.com/crtahlin/wasp/pull/343))
- measure the per-peer provider threshold ([#327](https://github.com/crtahlin/wasp/pull/327))
- keep both ledger rows ([#343](https://github.com/crtahlin/wasp/pull/343))
- why a sole-source provider download truncates ([#343](https://github.com/crtahlin/wasp/pull/343))
- record the credit gate facts ([#343](https://github.com/crtahlin/wasp/pull/343))
- how bandwidth is paid for ([#350](https://github.com/crtahlin/wasp/pull/350))
- which accounting terms move on a failing download ([#356](https://github.com/crtahlin/wasp/pull/356))
- spec for making overdraft refusals visible ([#354](https://github.com/crtahlin/wasp/pull/354))
- the gate terms measured at the refusal ([#358](https://github.com/crtahlin/wasp/pull/358))
- compare against origin/main, never the local main ref ([#360](https://github.com/crtahlin/wasp/pull/360))
- ledger row for the overdraft refusal line ([#361](https://github.com/crtahlin/wasp/pull/361))
- two nodes produce the same reference ([#341](https://github.com/crtahlin/wasp/pull/341))
- spec continuous accrual of the refresh allowance ([#359](https://github.com/crtahlin/wasp/pull/359))
- name the pinned commit without a moving ref beside it ([#355](https://github.com/crtahlin/wasp/pull/355))
- spec hosting a directory or website without a stamp ([#340](https://github.com/crtahlin/wasp/pull/340))
- record the new Bee-origin issue and correct a stale title ([#366](https://github.com/crtahlin/wasp/pull/366))
- warn that a closing keyword closes the issue ([#359](https://github.com/crtahlin/wasp/pull/359))
- spec carrying the preferred set into erasure coding ([#299](https://github.com/crtahlin/wasp/pull/299))
- amending does not undo a closing keyword ([#359](https://github.com/crtahlin/wasp/pull/359))
- record what the bench showed this morning ([#369](https://github.com/crtahlin/wasp/pull/369))
- ledger row for hosting a directory without a stamp ([#340](https://github.com/crtahlin/wasp/pull/340))
- say why local ingest computes redundancy it cannot use ([#375](https://github.com/crtahlin/wasp/pull/375))
- measured results for hosting a directory ([#340](https://github.com/crtahlin/wasp/pull/340))
- spec giving discovery a lifetime it owns ([#369](https://github.com/crtahlin/wasp/pull/369))
- ledger row for giving discovery its own lifetime ([#369](https://github.com/crtahlin/wasp/pull/369))
- measured results for giving discovery its own lifetime ([#369](https://github.com/crtahlin/wasp/pull/369))
- ledger row for the erasure-coded preferred set ([#299](https://github.com/crtahlin/wasp/pull/299))
- spec counting a provider connect that needed no dial ([#382](https://github.com/crtahlin/wasp/pull/382))
- ledger row for counting a provider connect ([#382](https://github.com/crtahlin/wasp/pull/382))
- ledger row for continuous accrual ([#359](https://github.com/crtahlin/wasp/pull/359))
- spec waiting for a provider's credit ([#392](https://github.com/crtahlin/wasp/pull/392))
- spec keeping a provider on a chunk it alone holds ([#392](https://github.com/crtahlin/wasp/pull/392))
- ledger row for provider retention ([#392](https://github.com/crtahlin/wasp/pull/392))
- arm 1 measured a restart, not retention ([#398](https://github.com/crtahlin/wasp/pull/398))
- spec not waiting for the network radius ([#398](https://github.com/crtahlin/wasp/pull/398))
- spec for the shutdown-race fix ([#403](https://github.com/crtahlin/wasp/pull/403))
- ledger row for the radius wait ([#398](https://github.com/crtahlin/wasp/pull/398))
- ledger and upstream rows for the shutdown-race fix ([#406](https://github.com/crtahlin/wasp/pull/406))
- measured results for continuous accrual ([#359](https://github.com/crtahlin/wasp/pull/359)) ([#391](https://github.com/crtahlin/wasp/pull/391))
- spec for the TestCrashRecovery bucket collision ([#408](https://github.com/crtahlin/wasp/pull/408))
- spec for the malformed index-document status code ([#411](https://github.com/crtahlin/wasp/pull/411))
- spec for keeping the dispersed-replica error chain ([#412](https://github.com/crtahlin/wasp/pull/412))
- spec for the threshold growth reconnect reset ([#413](https://github.com/crtahlin/wasp/pull/413))
- spec for protecting static peers from pruning ([#414](https://github.com/crtahlin/wasp/pull/414))
- ledger and upstream rows for four merged bug fixes ([#419](https://github.com/crtahlin/wasp/pull/419))
- ledger and upstream rows for the static peer pruning fix ([#420](https://github.com/crtahlin/wasp/pull/420))
- spec for the non-tar body status code ([#421](https://github.com/crtahlin/wasp/pull/421))
- spec for the uncapped refresh allowance in settle ([#423](https://github.com/crtahlin/wasp/pull/423))
- ledger and upstream rows for the non-tar body status fix ([#425](https://github.com/crtahlin/wasp/pull/425))
- spec for the unlocked cheque allocation ([#426](https://github.com/crtahlin/wasp/pull/426))
- spec for interrupting evict and unreserve on shutdown ([#429](https://github.com/crtahlin/wasp/pull/429))
- ledger row for the chequebook allocation lock ([#432](https://github.com/crtahlin/wasp/pull/432))
- ledger and upstream rows for the last two bug fixes ([#434](https://github.com/crtahlin/wasp/pull/434))
- a hinted download does not wait for its dial ([#436](https://github.com/crtahlin/wasp/pull/436))
- withdraw both fixes for the first hinted request ([#437](https://github.com/crtahlin/wasp/pull/437))
- spec not spending the error budget while a provider answers ([#439](https://github.com/crtahlin/wasp/pull/439))
- ledger and upstream rows for the flight exit fix ([#442](https://github.com/crtahlin/wasp/pull/442))
- the widened peer walk does not arrive ([#443](https://github.com/crtahlin/wasp/pull/443))
- spec measuring the refresh rate split ([#445](https://github.com/crtahlin/wasp/pull/445))
- a faster grant buys no bytes, closing #444 neutral ([#446](https://github.com/crtahlin/wasp/pull/446))
- spec answering 404 when the peer walk is depleted ([#447](https://github.com/crtahlin/wasp/pull/447))
- ledger and upstream rows for the chunk not-found fix ([#450](https://github.com/crtahlin/wasp/pull/450))
- spec the same not-found defect on POST /pins ([#451](https://github.com/crtahlin/wasp/pull/451))
- spec 400 for two malformed multipart uploads ([#453](https://github.com/crtahlin/wasp/pull/453))
- ledger and upstream rows for the two api status fixes ([#456](https://github.com/crtahlin/wasp/pull/456))
- spec bounding Close by the shutdown budget ([#457](https://github.com/crtahlin/wasp/pull/457))
- ledger row for the Close shutdown fix ([#459](https://github.com/crtahlin/wasp/pull/459))
- spec reordering the peer migration writes ([#460](https://github.com/crtahlin/wasp/pull/460))
- ledger and upstream rows for the withdrawn peer migration ([#463](https://github.com/crtahlin/wasp/pull/463))
- spec the last two malformed multipart bodies ([#464](https://github.com/crtahlin/wasp/pull/464))
- measure the hinted cold start, and spec the fix ([#465](https://github.com/crtahlin/wasp/pull/465))
- ledger and upstream rows for the multipart fix ([#467](https://github.com/crtahlin/wasp/pull/467))
- validate the preferred rebuild on the bench ([#470](https://github.com/crtahlin/wasp/pull/470))
- re-check the comparison point before a release ([#473](https://github.com/crtahlin/wasp/pull/473))
- spec leading the user agent with wasp ([#475](https://github.com/crtahlin/wasp/pull/475))
- ledger row for the wasp user agent ([#477](https://github.com/crtahlin/wasp/pull/477))

### Features
- writebench and the write-throughput result ([#195](https://github.com/crtahlin/wasp/pull/195))
- evictbench and the mass-eviction result ([#196](https://github.com/crtahlin/wasp/pull/196))
- eviction responsiveness flips the verdict ([#197](https://github.com/crtahlin/wasp/pull/197))
- reads under sync load, and cache-sensitivity ([#199](https://github.com/crtahlin/wasp/pull/199))
- decouple batch reconciliation from the wake-up scan ([#201](https://github.com/crtahlin/wasp/pull/201))
- pause pulling during reserve sampling ([#203](https://github.com/crtahlin/wasp/pull/203))
- make pebble the default index engine for a fresh datadir ([#205](https://github.com/crtahlin/wasp/pull/205))
- concurrent reads via a read/close lifecycle guard ([#8](https://github.com/crtahlin/wasp/pull/8))
- expose physical write bytes metric ([#218](https://github.com/crtahlin/wasp/pull/218))
- raise and expose reserve doubling cap ([#222](https://github.com/crtahlin/wasp/pull/222))
- configurable redistribution sync-rate threshold ([#223](https://github.com/crtahlin/wasp/pull/223))
- probe-based sample benchmark endpoint ([#241](https://github.com/crtahlin/wasp/pull/241))
- discover stake left in retired staking contracts ([#258](https://github.com/crtahlin/wasp/pull/258))
- recover stranded stake from retired contracts ([#259](https://github.com/crtahlin/wasp/pull/259))
- opt-in startup stake recovery and no-gas handling ([#260](https://github.com/crtahlin/wasp/pull/260))
- seed the historical staking-contract catalog ([#264](https://github.com/crtahlin/wasp/pull/264))
- windowed reserve sampler behind reserve-proof-mode ([#273](https://github.com/crtahlin/wasp/pull/273))
- route the redistribution agent by reserve-proof-mode ([#273](https://github.com/crtahlin/wasp/pull/273))
- configurable reserve-capacity ([#283](https://github.com/crtahlin/wasp/pull/283))
- expose L0 read-amplification metrics ([#286](https://github.com/crtahlin/wasp/pull/286))
- content providers, phase 1 ([#293](https://github.com/crtahlin/wasp/pull/293))
- grant a larger payment threshold per peer ([#327](https://github.com/crtahlin/wasp/pull/327))
- store content locally without postage ([#326](https://github.com/crtahlin/wasp/pull/326))
- log the terms of an overdraft refusal ([#357](https://github.com/crtahlin/wasp/pull/357))
- store a directory or website locally without postage ([#340](https://github.com/crtahlin/wasp/pull/340))
- accrue the refresh allowance continuously ([#359](https://github.com/crtahlin/wasp/pull/359))

### Miscellaneous
- do not demand a release chore appear in its own changelog ([#472](https://github.com/crtahlin/wasp/pull/472))

### Reverted
- concurrent reads, merged on a false premise ([#8](https://github.com/crtahlin/wasp/pull/8))

### Testing
- concurrent reserve Put benchmark for the lock question ([#202](https://github.com/crtahlin/wasp/pull/202))
- measure index write amplification ([#213](https://github.com/crtahlin/wasp/pull/213))
- spike the namespace-split write amplification ([#214](https://github.com/crtahlin/wasp/pull/214))
- measure cache-churn write amplification ([#216](https://github.com/crtahlin/wasp/pull/216))
- a failed peer migration must stay recoverable ([#461](https://github.com/crtahlin/wasp/pull/461))
# Changelog

Changes this fork makes to upstream Bee. Upstream's own changes are absorbed as
single `chore(upstream)` entries, see `.upstream-base` for the release this
build derives from, and upstream's release notes for what it contains.

## [0.1.3](https://github.com/crtahlin/wasp/releases/tag/v0.1.3) - 2026-09-01


### Documentation
- selectable index-store engine, and the Pebble A/B ([#186](https://github.com/crtahlin/wasp/pull/186))
- how to select the storage engine and switch keeping identity ([#188](https://github.com/crtahlin/wasp/pull/188))

### Features
- make the index-store engine selectable (leveldb|pebble) ([#187](https://github.com/crtahlin/wasp/pull/187))
## [0.1.2](https://github.com/crtahlin/wasp/releases/tag/v0.1.2) - 2026-08-31


### Bug fixes
- make the chunkstore benchmarks measure hits ([#171](https://github.com/crtahlin/wasp/pull/171))
- make missing-key lookups reach the store ([#174](https://github.com/crtahlin/wasp/pull/174))
- let a wedged dial stop holding up shutdown ([#175](https://github.com/crtahlin/wasp/pull/175))
- let wasp upgrade a bee-experimental install ([#178](https://github.com/crtahlin/wasp/pull/178))
- stop charging harness overhead to the store reads ([#181](https://github.com/crtahlin/wasp/pull/181))

### Documentation
- microbenchmarks drift with position in the process ([#182](https://github.com/crtahlin/wasp/pull/182))

### Features
- log when the index store stops accepting writes ([#180](https://github.com/crtahlin/wasp/pull/180))
- make the index store's level-0 triggers configurable ([#183](https://github.com/crtahlin/wasp/pull/183))
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
