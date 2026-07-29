# Governance

ghostgpu is an early-stage project with a single maintainer. This document describes how decisions are made today and how that is intended to change as the project grows. It is deliberately honest about the current state rather than describing a committee that does not exist.

## Current state: single maintainer

[@santimillang](https://github.com/santimillang) is currently the sole maintainer and makes final decisions on scope, design, and releases.

In practice this means:

- Design decisions are recorded in `docs/superpowers/specs/` before implementation, so the reasoning is reviewable even when only one person made the call.
- Significant changes go through a pull request with CI, not direct commits to `main`.
- Anyone may open an issue to challenge a decision. Disagreement recorded in public is more useful than consensus assumed in private.

## Becoming a maintainer

We want more maintainers. The path is intentionally simple:

1. Contribute meaningfully over time — code, review, documentation, or triage.
2. Demonstrate good judgment about scope. Knowing what *not* to build matters more here than volume of commits.
3. An existing maintainer nominates you by opening an issue. Approval requires agreement from all current maintainers and no sustained objection from active contributors.

Maintainers are listed in [MAINTAINERS.md](MAINTAINERS.md).

Maintainers who are inactive for six months may be moved to emeritus status. This is administrative, not a judgment — people's circumstances change, and stale permissions are a security liability.

## Decision making

For everyday changes: lazy consensus. A pull request with an approving review and green CI can merge. If someone requests changes, address them or make the case for why not.

For decisions that are hard to reverse — API group or CRD schema changes after release, license changes, dependency additions with large maintenance surface, changes to the safety invariant — open an issue with the `design` label and allow at least 72 hours for comment before acting.

If maintainers disagree and cannot resolve it, the change does not land. Defaulting to "no" for contested irreversible decisions is deliberate.

## Scope

The project's scope is defined by the design spec in `docs/superpowers/specs/`. Proposals that expand scope should update the spec in the same pull request, including what is explicitly *not* being simulated. A simulator that quietly grows unbounded fidelity claims becomes untrustworthy.

## Code of Conduct

All participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md). Maintainers are responsible for enforcement.

## Future

If ghostgpu attracts real adoption, the intent is to move toward a neutral home — a foundation such as CNCF — which requires vendor-neutral governance, multiple maintainers from different affiliations, and a documented release process. This document will be replaced at that point.
