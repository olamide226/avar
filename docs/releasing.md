# Releasing avar

Releases are created automatically after the `CI` workflow succeeds for a
commit merged to `main`. Do not push a release tag by hand: the Release workflow
calculates the version, creates the annotated tag, publishes the GitHub release,
and updates the Homebrew cask in one serialized job.

## Version policy

The release version is calculated from Conventional Commit subjects since the
latest stable `vMAJOR.MINOR.PATCH` tag. Pre-release tags do not set the baseline.

| Commit form | Release |
| --- | --- |
| `feat:` | Minor version |
| `fix:` or `perf:` | Patch version |
| Any `type!:` subject or `BREAKING CHANGE:` footer | Major version |
| `docs:`, `test:`, `chore:`, `ci:`, and other types | No release |

The highest applicable change wins. For example, a range containing both `fix:`
and `feat:` commits produces a minor release. A breaking change follows strict
Semantic Versioning, so it advances `v0.1.0` to `v1.0.0`.

## What the release workflow does

1. Waits for the existing macOS CI workflow to pass on `main`.
2. Checks out the exact commit CI tested and calculates the next version.
3. Creates the release tag, refusing to overwrite an existing tag.
4. Runs GoReleaser to build the universal macOS archive, publish the GitHub
   release, and update `olamide226/homebrew-tap/Casks/avar.rb`.

The workflow uses one job for tagging and publishing because a tag pushed with
GitHub's default workflow token does not start a second workflow.

## First-release recovery

If a tag exists but its GitHub release failed, do not create a new tag with the
same version. First repair the cause, then deliberately delete and recreate the
unpublished tag at a new commit, or choose a later version. Verify the GitHub
release assets and generated Homebrew cask before updating public installation
documentation.
