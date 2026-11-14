# Audiovisual Sync Conformer

## Goal

Audiovisual Sync Conformer is a self-hosted Go service for finishing
teams that need to measure and correct timing drift between a recorded audio
stream and an uncompressed picture stream. A take combines one 16-bit PCM WAV
file with one planar 4:2:0 Y4M file. The service detects the strongest paired
audio transient and picture flash, records an immutable sync analysis, and
produces a review bundle whose audio is aligned to picture timing.

The service is deliberately narrow. It does not edit creative timing, detect
general scene cuts, mix multiple recordings, or replace a non-linear editor. It
provides a deterministic first-pass conform and an auditable receipt that an
assistant editor can inspect before moving the material into a finishing
system.

## Users

- Assistant editors reviewing dual-system sound before editorial turnover.
- Finishing engineers validating camera and recorder clock drift.
- Review operators producing a bounded proxy bundle for a sync decision.

## Entities and invariants

### Media take

A `MediaTake` contains one immutable WAV payload, one immutable Y4M payload,
their media descriptors, a combined content digest, and an admitted timestamp.
The stored digest always describes the exact two payloads. A take identifier is
unique and may be retried only with the same name, audio bytes, and picture
bytes.

### Sync analysis

A `SyncAnalysis` belongs to one admitted take. It records:

- the detected audio transient time in milliseconds;
- the detected picture flash time in milliseconds;
- the signed offset, where a positive value means audio is late;
- a confidence value and a drift estimate in parts per million;
- the threshold policy used for the decision.

An analysis is immutable after it reaches `ready` or `failed`. A `ready`
analysis must have a finite offset and a positive event prominence. A failed
analysis stores one stable error code and never exposes a usable event pair.

### Conform job

A `ConformJob` belongs to one ready analysis. It corrects the audio by the
inverse of the measured offset while preserving picture frame order and Y4M
geometry. The job commits a ZIP bundle containing:

- `audio-aligned.wav`;
- `video-conformed.y4m`;
- `conform-receipt.json`.

The completed receipt digest and byte count always match the visible ZIP.
Failed jobs never expose a bundle route.

### Conform program

A `ConformProgram` joins several received materials, in review order, into one
continuous picture stream and one continuous audio track. Each entry is one
segment backed by its own ready `SyncAnalysis`, so every segment keeps the
sync basis measured for its own material. A segment may select a half-open
picture interval of its source take; without an interval it uses the whole
take.

The program renders all segments on one cumulative audio sample timeline.
Each junction is the difference of two cumulative boundaries rather than an
independently rounded segment duration, so the junction windows tile the exact
total length: no audio is added or dropped where segments meet, and the final
boundary equals the whole-program boundary regardless of segment count. Each
segment is still aligned by the inverse of its own measured offset.

All segments must share sample rate, channel count, picture geometry, frame
rate, and chroma. The completed ZIP contains:

- `audio-program.wav`;
- `video-program.y4m`;
- `conform-program-receipt.json`.

The receipt records, per segment, the source take, source picture interval,
measured and applied offset, applied samples, and output timeline placement,
so every delivered frame and audio window is traceable to its source. The
receipt digests and byte count always match the visible ZIP. A failed program
never exposes a bundle route or a partial package.

## State machines

### Media take

`validating -> admitted`

Validation failures do not create a visible take. Repeating an identical
create request returns the admitted record. Reusing an identifier with
different bytes or a different name returns a conflict.

### Sync analysis

`queued -> detecting -> ready`

or

`queued -> detecting -> failed`

`ready` and `failed` are terminal. The service persists each state before
continuing, so a reader can distinguish an in-progress analysis from a
completed one.

### Conform job

`queued -> rendering -> committing -> completed`

or

`queued -> rendering -> failed`

or

`rendering -> committing -> failed`

`completed` and `failed` are terminal. The bundle and receipt are written in a
temporary directory and become visible only through one directory rename.

### Conform program

`queued -> rendering -> committing -> completed`

or

`queued -> rendering -> failed`

or

`rendering -> committing -> failed`

It uses the same terminal rules and atomic directory publication as a conform
job. A correction above the ceiling is recorded as a terminal failed program
after the queued record is persisted, so an operator can read the failure even
though no bundle exists.

## Workflows

### 1. Admit a media take

Entry point: `POST /v1/takes`

1. Validate the identifier, name, size budgets, and strict base64 encoding.
2. Probe every WAV chunk and every Y4M frame before accepting the take.
3. Measure media descriptors and compute a combined digest.
4. Persist payloads and metadata under an exclusive take namespace.

Success returns the immutable take record. Failure returns a stable validation
or conflict code and leaves no visible take directory.

### 2. Detect audiovisual drift

Entry point: `POST /v1/takes/{take_id}/sync-analyses`

1. Load the admitted take and create a queued analysis.
2. Decode both media payloads again at the analysis boundary.
3. Detect a sharp audio onset and a picture luma flash.
4. Compare event times against duration and confidence limits.
5. Persist a ready or failed terminal record.

Success returns measured event times, signed offset, confidence, and drift.
Failure records a stable code such as `sync_detection_failed`.

### 3. Render a conformed review bundle

Entry point: `POST /v1/sync-analyses/{analysis_id}/conforms`

1. Require a ready analysis and enforce the requested correction ceiling.
2. Align audio samples by the inverse measured offset.
3. Re-emit the Y4M header and frames with validated geometry.
4. Build the receipt, ZIP bundle, and digests in a temporary directory.
5. Commit the directory atomically and complete the job.

Success returns a completed job whose receipt and bundle routes agree. Failure
returns a terminal job with a stable error code and no readable bundle.

### 4. Join segments into a continuous conform program

Entry point: `POST /v1/conform-programs`

1. Validate the identifier, correction ceiling, segment count, and every
   half-open picture interval.
2. Resolve each segment's ready analysis and source take; require one distinct
   sync basis per segment and matching media geometry across segments.
3. Persist a queued program, then reject any segment whose correction exceeds
   the ceiling as a terminal failed program.
4. Align each segment by its own offset and join audio and picture on one
   cumulative sample timeline.
5. Build the per-segment receipt, ZIP bundle, and digests in a temporary
   directory.
6. Commit the directory atomically and complete the program.

Success returns a completed program whose receipt traces every segment to its
source take and interval and whose bundle route agrees. Failure returns a
terminal program with a stable error code and no readable, partial bundle.

## Modules

- `internal/httpapi`: public routing, JSON decoding, request limits, and error
  status mapping.
- `internal/service`: workflow orchestration, idempotency, lifecycle
  transitions, and failure propagation.
- `internal/domain`: identifiers, records, state rules, and stable errors.
- `internal/media`: WAV and Y4M probing, signal detection, audio alignment, and
  deterministic media encoding.
- `internal/storage`: namespaced persistence, per-identifier locking, atomic
  files, and atomic directory publication.

The `admit-take` workflow crosses all five modules. The other two workflows
cross the HTTP boundary, service orchestration, media processing, domain rules,
and storage.

## Public interface

The service listens on `/healthz` and these resource routes:

- `POST /v1/takes`
- `GET /v1/takes/{take_id}`
- `POST /v1/takes/{take_id}/sync-analyses`
- `GET /v1/sync-analyses/{analysis_id}`
- `POST /v1/sync-analyses/{analysis_id}/conforms`
- `GET /v1/conforms/{conform_id}`
- `GET /v1/conforms/{conform_id}/bundle`
- `POST /v1/conform-programs`
- `GET /v1/conform-programs/{program_id}`
- `GET /v1/conform-programs/{program_id}/bundle`

JSON errors use a stable envelope:

```json
{
  "error": {
    "code": "invalid_media",
    "message": "video decoder rejected frame payload"
  }
}
```

## Validation plan

The initial baseline intentionally defers automated tests. A later task stage
will add unit, integration, and HTTP verification for the production
interfaces below:

- WAV chunk bounds and Y4M frame-count agreement with declared geometry;
- onset and flash detection on calibration media with known event times;
- positive and negative audio corrections around the measured offset;
- terminal-state behavior for invalid and boundary requests;
- atomic visibility of take directories and conform bundles.

Until that stage, bounded production workflow checks exercise the real
HTTP routes, storage layout, media transforms, and persisted terminal records.
They are not a substitute for the later test suite. The checks cover take
admission, sync analysis, single-take conform, and the multi-segment conform
program, including shared-timeline continuity, per-segment traceability, and
the absence of a partial bundle on failure.

## Deferred testing boundary

Automated tests are intentionally omitted from this baseline. The later task
stage owns creation of the test harness, fixtures, race checks, and test
command. This repository ships only executable production workflow checks.
