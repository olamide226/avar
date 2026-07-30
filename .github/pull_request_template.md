# Summary

<!-- What this PR does and why, in prose. Name the spec task it implements. -->

**Spec task:** <!-- e.g. tasks.md task 4 — Implement Resolver -->

## Requirements covered

<!--
Every acceptance criterion this PR implements, cited as REQ-<requirement>.<criterion>
from .kiro/specs/avar-cli/requirements.md. Quote the criterion so a reviewer can
check the diff against it without opening the spec, and say how the code satisfies it.
-->

| Requirement | Acceptance criterion | How this PR satisfies it |
| --- | --- | --- |
| REQ-x.y | <!-- criterion text --> | <!-- implementation note + where --> |

## Correctness properties

<!-- Properties from design.md §5 this PR establishes or tests, e.g. PROP-2. -->

| Property | Where it is proven |
| --- | --- |
| PROP-n | <!-- test name / file --> |

## Deliberately not covered

<!--
Criteria in scope of the same requirement that this PR does NOT finish, and which
task picks them up. Say "none" if the requirements above are fully satisfied.
-->

## Verification

<!-- What you actually ran, with results. Not a plan — an outcome. -->

- [ ] `make lint`
- [ ] `make test`
- [ ] `make e2e` <!-- state "not run: requires local Lima" if that is the case -->

## Notes for review

<!--
Anything a reviewer would otherwise have to reconstruct: trade-offs taken,
alternatives rejected, files touched outside this task's `_writes:` manifest,
spec amendments made in this PR.
-->
