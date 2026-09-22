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

## After a release: check the Release run, not the release page

GoReleaser publishes the GitHub release first and the package managers after
it. When a package manager step fails, the release page, the tag and the
archives all look finished while the Release workflow is red. From v0.3.0 to
v0.9.0 every Release run failed that way: the Homebrew tap token returned 401,
so `brew install` served v0.2.1 for over two weeks. Nothing surfaced it,
because the CI badge reports CI, not Release.

After a `feat:` or `fix:` merge, open the Release run and confirm it is green.
If it failed after "release published", do not re-tag. Fix the cause (usually
a token), and the next release publishes to every channel. The GitHub release
that already exists is fine as it is.

## winget

**Submissions are paused** (2026-09-22): `skip_upload` in `.goreleaser.yaml`
is `true`, so no release opens a pull request on `microsoft/winget-pkgs`. The
package's first submission is still queued for a moderator, and every minor
release opened another beside it. Restore the commented line in that file when
the package is accepted, and the paragraph below describes what happens again.

Until then, each release still generates the manifests into `dist/winget/`,
so a mistake in the packaging shows up in the release run rather than in front
of Microsoft.

Every stable minor or major release (x.y.0) pushes manifests for
`olamide226.avar` to a branch
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

Patch releases are not submitted. Each submission is reviewed by people at
Microsoft, and releasing on every `fix:` merge once opened five pull requests
in 45 minutes for a package still in its first review. If a patch fixes
something Windows users need through winget, cut a minor release instead.
Open submissions are not tracked automatically, so if several minor releases
land while one is still in review, close the older pull requests as superseded.

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
