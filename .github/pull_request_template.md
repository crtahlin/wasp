## What this changes

<!-- One paragraph. This becomes the merge subject's context, not a replacement for it. -->

Closes #

## Spec

<!--
Link the merged spec: docs/experiments/<slug>/spec.md
Experiments and optimizations need a merged spec BEFORE implementation.
Exempt: docs-only changes, chores, and upstream syncs — say which applies.
-->

## Protocol impact

<!--
Answer this, do not tick it. See docs/agent-playbooks/protocol-compatibility.md.

Does this touch the frozen wire surface — the "/swarm/" stream prefix, any
protocolName/protocolVersion, the handshake proto, chunk geometry in pkg/swarm,
or NetworkID/ChainID in pkg/config?

If NO: say why you are confident. "I did not edit those files" is a fine answer.
If YES: say which peers stop interoperating and what the operator-visible symptom
would be, then apply the `protocol-change` label and regenerate the lock file.
-->

## How this was verified

<!--
- [ ] `make format && make build && make test && make lint` pass locally
- [ ] Tests added or updated for the behaviour that changed
- [ ] Documentation updated in this same PR
- [ ] Measured on the bench, numbers recorded in docs/experiments/<slug>/measurement.md
      (or: state that this is not a performance claim)
- [ ] Ran on a real node against the network, and it peered and stayed healthy
      (an HTTP 200 is not proof — check /health, /readiness, /status, /peers, and the logs)
-->

## Upstream portability

<!--
What would Ethersphere need in order to take this? Is it self-contained, does it
depend on other fork changes, does it need a config flag they would want to
default differently? This is what makes the work reusable by someone else.
-->

---

Generated with help of AI.

<!--
Remove the line above if this pull request was written without AI assistance.
See AGENTS.md.

Before merging:
  gh pr merge <n> --merge --subject "type(scope): what changed (#<n>)"
The subject IS the changelog entry. Squash and rebase merging are disabled.
Do not delete the branch. Tag the merge commit exp/<slug> and add the ledger row.
-->
