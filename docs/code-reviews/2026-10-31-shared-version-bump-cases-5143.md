# Review: version-bump test on the shared cases file (ut-docs#5143)

Date: 2026-10-31. Lane: lane:cloud-41. Full record: ut-docs
`code-reviews/2026-10-31-shared-version-bump-cases-5143.md`.

- `scripts/check-version-bump.test.sh` now keeps only this repo's fixture
  (`vb_fixture`, `VB_VARIANT`, `VB_SHIPPED_FILE`) and its entries-mirror case.
  The shared cases come from `scripts/check-version-bump.cases.sh`, a
  byte-identical copy of the ut-docs canonical file that checks its own hash.
  Never edit it here.
- Every old case still runs with the same or stricter assertions, plus one new
  case: a hand-edited cases copy fails.
- Verified: the test passes, and it fails without the cases file. Dropping a
  SHIPPED_PATTERNS entry turns it red. shellcheck is clean. The guard sees no
  shipped change, so no version bump.
- Independent review (Sonnet; author Opus): merge, no blockers. Its minors
  were fixed in the canonical file and re-synced here.
