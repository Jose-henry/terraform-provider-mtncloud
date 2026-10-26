## What and why

<!-- One resource or one coherent slice. Link the decision (D-NNN) or backlog item. -->

## API source

<!-- Context7 page(s) and the captured payload / fixture this is built from. -->

## Checklist

- [ ] Plan was approved before coding
- [ ] `make ci` passes locally
- [ ] Tests: unit, client contract, and acceptance steps (basic + empty plan, update, import, disappears, drift) where applicable
- [ ] New tests fail when the behaviour is broken (mutation-checked)
- [ ] Docs regenerated (`make docs`) and example added or updated
- [ ] No secrets, tfvars, state, or real tenant IDs/IPs in the diff
- [ ] Master-only types are gated and say so in their description
- [ ] `mtncloud-code-reviewer` ran; findings fixed or answered
