# Results: announcing in the same call as a local ingest

Issue: [#503](https://github.com/crtahlin/wasp/issues/503). Spec:
[ingest-announce.md](ingest-announce.md). Merged as `26078d70` (#532), tag
`exp-ingest-announce`.

## Outcome

**Validated.** One `POST /wasp/ingest` with a usable batch stored and announced
fresh content in 3 of 3 runs. A requester that was given only the reference
found the provider and downloaded the right bytes with a plain GET each time. With
a batch that does not exist, the ingest answered 201 and reported the failed
announcement in 3 of 3 runs, and the content stayed stored and could be
announced afterwards.

## Setup

- **Provider: `bench-1`** on `main` at `26078d70`. The spec named `stake-1`,
  but `stake-1` runs tagged releases only and no release carries this change
  yet.
- **Requester: `stake-1`** on v0.1.5, which looks the providers up when a
  download cannot fetch its root chunk (#498). It was disconnected from
  `bench-1` before each lookup and before each download, so it could reach the
  provider only through the record.
- 500,000 bytes of fresh random data per run, redundancy level 0, 2026-09-26.
  A depth-17 batch bought on `bench-1` for the purpose.

## One call

**Table: `POST /wasp/ingest` with `Swarm-Postage-Batch-Id` on `bench-1`, then a plain download on `stake-1`**

| Run | Ingest | `announced` | Listed in `GET /wasp/providers` | `stake-1` lookup finds `bench-1` | `stake-1` plain GET | Bytes match |
|---|---|---|---|---|---|---|
| 1 | 201 | true | yes | yes | 200 in 3.0 s | yes |
| 2 | 201 | true | yes | yes | 200 in 3.2 s | yes |
| 3 | 201 | true | yes | yes | 200 in 3.0 s | yes |

## A batch that does not exist

**Table: the same ingest with a batch ID that no batch has, then a later announcement with the real batch**

| Run | Ingest | `announced` | `announceError` | Content pinned | Listed before | Later `POST /wasp/providers/{ref}` | Listed after |
|---|---|---|---|---|---|---|---|
| 1 | 201 | false | batch with id not found | yes | no | 201 | yes |
| 2 | 201 | false | batch with id not found | yes | no | 201 | yes |
| 3 | 201 | false | batch with id not found | yes | no | 201 | yes |

## Was the content stamped?

The spec asked for the batch's utilization before and after. It was 1 before
and 1 after the three announced ingests. That is weak evidence: utilization is
the count in the batch's fullest bucket, and about 125 chunks per run spread
over the buckets of a depth-17 batch would most likely not have raised it
either. The stronger evidence is the code: the ingest's putter carries no
stamper, which #532 did not change, and the announcement stamps only the record
through the same path as `POST /wasp/providers/{reference}`.

## Not measured on a node

The refusals before the body is read (providers off, a light node, an encrypted
ingest) and the collection case are covered by tests in
`pkg/api/localingest_announce_test.go`, not by a node run.

Generated with help of AI.
