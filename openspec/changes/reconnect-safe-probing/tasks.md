# Implementation plan

- [x] Add failing scheduler regressions for reconnect, minimum spacing, backoff, restart, channel independence, discovery and manual semantics.
- [x] Persist independent per-interface throttle and reserve before payload; preserve invalidation and finite calibration.
- [x] Make status deadlines match all automatic guards and accommodate existing short settle/window configuration.
- [x] Update English/Russian documentation and 0.1.6 package metadata/fixtures.
- [x] Run full Go, race, vet, syntax and relevant fixtures; self-review.

- [x] Fix review finding: publish measurement reservation atomically after successful persistence; verify refusal preserves counters/deadlines and repair permits the original first automatic measurement.

- [x] Fix review finding: allow five calibration attempts after reconnect with an inherited capped failure streak; reserve four maximum backoff gaps in the finite window.
