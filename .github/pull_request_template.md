<!--
Thanks for contributing to ghostgpu.

All commits must be signed off (DCO): git commit -s
See CONTRIBUTING.md for the full guide.
-->

## What this changes

<!-- What does this do, and why? Link the issue if there is one: Fixes #123 -->

## Why this approach

<!-- What alternatives did you consider and reject? This is the most useful
     part of a PR description for a future reader. Delete if trivial. -->

## Testing

<!-- Tick what applies, and say what you added. -->

- [ ] Unit tests (pure logic — device identity, object construction, formatting)
- [ ] envtest (needs a real API server — CRD schema, defaulting, validation)
- [ ] e2e (needs a real scheduler — anything asserting scheduling *decisions*)
- [ ] Manually verified against a kwok cluster

<!-- Reminder: envtest runs no kube-scheduler. If your change affects what the
     scheduler does, it needs an e2e test. -->

## Fidelity impact

<!-- Does this add or change simulated behavior? Classify it:
     - Faithful    — byte-comparable to the real thing
     - Approximated — plausible, not measured from hardware
     - Not simulated — explicitly out of scope
     Delete this section if the change does not touch simulation behavior. -->

## Checklist

- [ ] Commits are signed off (`git commit -s`)
- [ ] `make test` passes
- [ ] `make lint` passes
- [ ] `make manifests generate` run and result committed, if APIs changed
- [ ] Docs/spec updated, if behavior or scope changed
- [ ] `CHANGELOG.md` updated under `[Unreleased]`, if user-visible

## Safety

- [ ] This change does **not** weaken the simulated-node invariant (`safety.IsSimulatedNode`)

<!-- If it does, say so explicitly here so it gets proper scrutiny. -->
