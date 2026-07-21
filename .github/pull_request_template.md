## Summary

Why does this change exist, what problem does it solve, and why is this the
right approach?

## Compatibility impact

- Source objects affected:
- Verdict or reason-code changes:
- Wire-format/API changes:
- Backward compatibility:

## Testing strategy

- Tests added or updated:
- Exact commands run:
- Corpus baseline change, if any:
- Live validation or screenshots, if applicable:
- Edge cases covered:

## Risk and recovery

- Blast radius:
- Potential regressions:
- Rollback plan:

## Checklist

- [ ] The change is focused and contains no unrelated cleanup.
- [ ] New behavior has a fixture, golden, corpus, or live evidence record.
- [ ] Every source object remains accounted for.
- [ ] Reason-code and compatibility documentation is updated.
- [ ] `make fmt vet test-race lint build` passes.
- [ ] Credentials, deployment inventory, and generated artifacts are excluded.
- [ ] The [guarantees](../docs/guarantees.md) invariant holds: nothing becomes
      `native` without passing the live differential.

## Notes for reviewers

Call out the files or assumptions that deserve the closest review.
