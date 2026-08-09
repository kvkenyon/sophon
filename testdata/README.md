# Test fixtures

Shared fixture data for cross-package tests belongs here. Package-local fixtures should use that package's own `testdata` directory.

- `home-111-blocked-result.json` preserves the rejected completion-shaped
  scope-mismatch evidence that must never derive ready.
- `pr-6818-open-correction.json` preserves the exact historical repository,
  public branch, PR 6818 identity, open head, and verified correction
  descendant that prove an already-public legacy branch can continue through
  the same PR without creating a replacement.
