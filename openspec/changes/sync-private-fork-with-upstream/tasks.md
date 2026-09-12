## 1. Preserve the Current Fork

- [x] 1.1 Inspect and record the current branch, commit, worktree changes, private commit range, and configured remotes without modifying unrelated work.
- [x] 1.2 Create a durable archive branch or tag for the current private `main` commit. (`archive/private-main-pre-v1.16` at `70858d3`.)
- [x] 1.3 Back up the existing `./data` directory, including `photoframe.db` and locally stored images, when it exists. (No `./data` directory existed.)

## 2. Establish the Upstream Base

- [x] 2.1 Add the canonical `aitjcize/esp32-photoframe-server` repository as the `upstream` remote and fetch its branches and tags.
- [x] 2.2 Verify that local commit `eec9040` and upstream v1.12.0 have identical trees, and record the exact upstream v1.16.0 target commit. (Tree `b789e525`; v1.16.0 commit `f0aa8007`.)
- [x] 2.3 Create a temporary integration branch that can be discarded without affecting the archived or private `main` branches. (`integrate/upstream-v1.16.0`.)

## 3. Replay the Private Changes

- [x] 3.1 Replay the 13 private commits after `eec9040` onto upstream v1.16.0. (Eleven replayed; two skipped because upstream already contains their behavior.)
- [x] 3.2 Resolve backend and webapp conflicts by retaining upstream as the baseline while preserving existing private behavior with the smallest necessary edits.
- [x] 3.3 Resolve Docker, dependency, lockfile, version, and CI conflicts using upstream versions unless a private feature requires otherwise.
- [x] 3.4 If an individual feature produces disproportionate conflicts, replace only that feature's replayed commits with a consolidated application of its existing net behavior. (No consolidation was required.)
- [x] 3.5 Compare the resulting private feature set with the archived branch and record unrelated defects for later work instead of fixing them in this change. (No unrelated defects were addressed.)

## 4. Reconcile Database Migrations

- [x] 4.1 Preserve upstream's battery and firmware migrations as `000036` and `000037`.
- [x] 4.2 Renumber the fork's Immich cache and device image queue migrations after the upstream sequence and verify their up/down pairing and migration discovery. (Renumbered to `000038` and `000039`; this repository uses filesystem discovery rather than embedded migrations.)
- [x] 4.3 Initialize a fresh Docker Compose data directory and confirm the complete migration chain succeeds on an empty SQLite database. (The migration integration test passed against a fresh SQLite database; Compose startup is tracked separately in 5.4.)

## 5. Verify the Integrated Fork

- [x] 5.1 Run Go formatting checks, build, and backend tests with the Go version required by the integrated `go.mod`. (Passed in `golang:1.24-alpine`.)
- [x] 5.2 Run `npm ci`, formatting checks, tests, type checking, and the production webapp build. (Passed in `node:20-alpine` with install scripts disabled, matching the Docker build stage.)
- [x] 5.3 Build the Docker Compose image from the integrated branch with no stale image or build cache masking failures. (`docker compose build --no-cache` passed.)
- [x] 5.4 Start Docker Compose against the fresh data directory and confirm migrations complete, the container remains healthy, and the web UI responds. (Used host port 19607 because the production v1.1.1 container owns 9607; container port remained 9607.)
- [x] 5.5 Smoke-test that the configuration surfaces for `epdoptimize`, server-side processing, Immich date filtering/cache, and per-device queues are still present and load without immediate errors.
- [x] 5.6 Stop the smoke-test deployment and retain its logs and verification results for review. (Logs retained under `/tmp/opencode/photoframe-*`.)

## 6. Prepare the Updated Private Main

- [x] 6.1 Add concise repository documentation describing the `origin`/`upstream` roles, release-based update procedure, migration-number check, and Docker Compose verification commands.
- [x] 6.2 Review the final diff and history against both upstream v1.16.0 and the archived private branch, confirming that changes are limited to integration needs.
- [x] 6.3 Move private `main` to the verified integration result without deleting the archive reference or original data backup.
- [x] 6.4 Confirm the final worktree and branch status are ready for user review; do not push unless separately requested.
