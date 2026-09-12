## Context

The private `main` branch contains 13 commits after local commit `eec9040`, whose tree is identical to upstream's rewritten v1.12.0 commit `b28626b`. The canonical upstream is now at v1.16.0 and has changed several of the same backend, webapp, Docker, and CI files as the fork.

The repository has only one user and is maintained for personal use. The integration should therefore favor a recoverable, understandable procedure over elaborate compatibility infrastructure. The existing private features are to remain unchanged unless conflict resolution or successful verification requires an adjustment.

Both histories use migration numbers `000036` and `000037` for different schema changes. The existing SQLite data may be backed up and replaced with a fresh database.

## Goals / Non-Goals

**Goals:**

- Establish the current upstream release as the ancestor of the private fork.
- Preserve the existing private commit series and observable feature behavior where practical.
- Produce a build that passes available automated checks and runs through Docker Compose.
- Leave a short, repeatable upstream-update procedure.
- Keep rollback possible throughout the integration.

**Non-Goals:**

- Proactively refactor or redesign private features.
- Fix known or suspected defects that do not block integration or verification.
- Preserve the current SQLite database in place at the cost of complex compatibility logic.
- Mirror every upstream commit or automate upstream synchronization.
- Prepare private changes for contribution to upstream.

## Decisions

### Target the latest upstream release

Integrate the upstream v1.16.0 release rather than following the tip of `upstream/main`. This provides a named, reproducible base and matches the intended release-based update cadence.

Following every upstream commit was rejected because it creates maintenance work without corresponding value for a personal project.

### Preserve origin and add a canonical upstream remote

Keep `origin` pointed at the private repository and add `upstream` for `aitjcize/esp32-photoframe-server`. Future private work continues on `origin/main`; upstream is read-only input.

Replacing `origin` or maintaining a separate permanent mirror branch was rejected as unnecessary.

### Replay the private commit range onto upstream

Create an archive reference for the current private `main`, then perform the integration on a temporary branch. Replay commits after `eec9040` onto upstream v1.16.0 using the known identical-tree base relationship. This avoids relying on Git to discover ancestry that upstream history rewriting obscured.

Resolve conflicts with these priorities:

1. Retain upstream changes as the new baseline.
2. Preserve the private feature's existing behavior.
3. Make the smallest change needed to combine them.
4. If preserving an individual commit causes disproportionate conflict, reapply that feature's net diff as a consolidated integration commit instead of reconstructing its old implementation line by line.

Starting from upstream and redesigning each private feature was rejected because the user wants the current changes retained. Merging the unrelated histories directly was rejected because it would obscure the known common tree and produce a harder-to-understand result.

### Resolve migration collisions by renumbering and rebuilding

Keep upstream migrations `000036` and `000037` under their canonical names. Renumber the fork's Immich cache and device image queue migrations to follow the upstream sequence. Back up the existing Docker Compose data directory, then verify the integrated code against a fresh database.

An in-place migration bridge and a second fork-specific migration system were rejected as excessive for a replaceable, single-user database. The backup remains available for manual recovery or later data extraction.

### Limit integration edits

Do not clean up code merely because a better implementation is apparent. Modify private code only when required by a conflict, changed upstream interface, migration ordering, build failure, automated test failure, or Docker Compose smoke-test failure.

Unrelated findings should be recorded for a later stabilization change rather than expanded into this one.

### Verify at three levels

Run backend tests with the Go version required by the integrated `go.mod`, install locked webapp dependencies and run its tests/type checking/build, then build and start the application through Docker Compose. The smoke test should confirm startup, database migration completion, web UI availability, and absence of immediate container errors.

This is intentionally narrower than comprehensive end-to-end testing.

### Use merge-based updates after reconstruction

After this one-time replay establishes upstream ancestry, integrate later useful upstream releases through a temporary update branch and merge the tested result into private `main`. Record the upstream release in the merge message or private release notes.

Repeatedly rebasing the long-lived private branch was rejected because stable deployed commit identities and one-time conflict resolution are more useful than perfectly linear history.

## Risks / Trade-offs

- [Private behavior changes accidentally during conflict resolution] -> Compare the integrated tree and user-visible configuration against the archived branch, and avoid unrelated edits.
- [The fresh database loses settings or locally stored images] -> Back up the entire Docker Compose data directory before testing and retain the archive until the new deployment is accepted.
- [Migration numbers collide again in a future upstream release] -> Treat private migration filenames as an explicit checkpoint during each release update; do not introduce a more elaborate migration system unless collisions become frequent.
- [Automated tests do not cover private cache and queue behavior] -> Require startup and focused manual smoke checks for retained private features, while deferring broad new test coverage to stabilization work.
- [Replaying commits produces extensive conflicts] -> Preserve behavior rather than exact commit shape and consolidate only the affected feature when that is simpler.
- [Upstream dependency changes alter generated lockfiles or image contents] -> Prefer upstream dependency versions unless a private feature demonstrably requires a different version.

## Migration Plan

1. Record the current worktree state and create a durable archive reference for the private `main` commit.
2. Back up the Docker Compose data directory, including the SQLite database and locally stored images.
3. Add and fetch the canonical `upstream` remote and identify the v1.16.0 release commit.
4. Create a temporary integration branch and replay the private commits after `eec9040` onto v1.16.0.
5. Resolve code and dependency conflicts minimally, using the archived branch for comparison.
6. Keep upstream migrations `000036` and `000037`, renumber the two private migrations after them, and initialize a fresh data directory.
7. Run backend and frontend checks, then build and smoke-test Docker Compose with the fresh database.
8. Confirm the retained private features are present and their configuration surfaces load.
9. Replace private `main` only after verification succeeds, while retaining the archive reference and data backup until the deployment is accepted.
10. Roll back by restoring the archived branch and original Docker Compose data directory if integration cannot be completed safely.
