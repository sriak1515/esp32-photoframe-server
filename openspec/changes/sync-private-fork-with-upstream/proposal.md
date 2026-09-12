## Why

The private fork is based on upstream v1.12.0 while upstream has continued to evolve. Synchronizing now will reduce future drift while preserving the fork's existing hobby-project features without turning maintenance into a broad stabilization or redesign effort.

## What Changes

- Configure the canonical repository as an `upstream` Git remote while retaining the private repository as `origin`.
- Preserve the current private branch and back up the SQLite data before integration.
- Rebase or replay the existing private commits onto the current upstream release, keeping their behavior and implementation unchanged except where integration requires conflict resolution.
- Resolve the collision between the fork's and upstream's `000036` and `000037` database migrations, with a fresh database permitted when that is the simplest safe path.
- Resolve build, dependency, and CI conflicts required to produce the private Docker image.
- Verify the integrated fork with automated checks and a Docker Compose smoke test.
- Document a lightweight process for integrating useful upstream releases in the future.
- Defer proactive refactoring, feature redesign, and unrelated bug fixing to later changes.

## Capabilities

### New Capabilities

None. This change preserves existing product behavior.

### Modified Capabilities

None. Upstream synchronization and repository maintenance do not intentionally change requirements.

## Impact

- Git history, remotes, and private integration branches.
- Files changed by both upstream and the private commits, particularly image handling, processing, Immich integration, device settings, Docker packaging, and CI.
- SQLite migration ordering and the existing private database.
- Go and webapp dependency versions and their automated checks.
- Docker Compose build and runtime verification.
