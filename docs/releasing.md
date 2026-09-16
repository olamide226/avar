# Releasing avar

Releases are created automatically after the `CI` workflow succeeds for a
commit merged to `main`. Do not push a release tag by hand: the Release workflow
calculates the version, creates the annotated tag, publishes the GitHub release,
updates the Homebrew cask, and submits the winget manifests in one serialized
job.

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
4. Runs GoReleaser to build the universal macOS archive and the
   `windows_amd64` and `windows_arm64` archives, publish the GitHub release,
   update `olamide226/homebrew-tap/Casks/avar.rb`, and submit the winget
   manifests (see below).

The workflow uses one job for tagging and publishing because a tag pushed with
GitHub's default workflow token does not start a second workflow.

## winget

Every stable release pushes manifests for `olamide226.avar` to a branch
`avar-<version>` of `olamide226/winget-pkgs`, a fork of
`microsoft/winget-pkgs`, and opens a pull request against Microsoft's
repository. Prereleases are not submitted.

Unlike the Homebrew tap, publishing is not finished when the workflow is. The
version reaches `winget install` only after Microsoft's validation pipeline
passes and the pull request merges. The first submission of the package also
gets a human review. If validation fails, the pull request says why: fix the
cause in `.goreleaser.yaml`, and the next release submits a corrected manifest.
Do not edit manifests by hand in the fork, because the next release overwrites
them.

This needs two things that live outside this repository:

- **The fork.** `olamide226/winget-pkgs`, forked from `microsoft/winget-pkgs`.
  GoReleaser creates a branch per version, so the fork's `master` does not need
  to be kept in sync.
- **The `WINGET_TOKEN` secret.** A classic personal access token with the
  `public_repo` scope, able to push to the fork and open a pull request on
  Microsoft's repository.

Without the secret, the release still succeeds. GoReleaser writes the
manifests to `dist/winget/` and submits nothing. To check a configuration
change without releasing, run `goreleaser release --snapshot --clean
--skip=before` and read the files under `dist/winget/manifests/`.

## First-release recovery

If a tag exists but its GitHub release failed, do not create a new tag with the
same version. First repair the cause, then deliberately delete and recreate the
unpublished tag at a new commit, or choose a later version. Verify the GitHub
release assets and generated Homebrew cask before updating public installation
documentation.
