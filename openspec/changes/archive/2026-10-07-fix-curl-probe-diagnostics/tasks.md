## 1. Regression and implementation
- [x] 1.1 Add a real-curl missing-file regression using the production write-out argument; verify it fails on the current implementation.
- [x] 1.2 Cover malformed/missing statistics with runner failure, no-body timeouts, successful transfers, valid capped samples, and persistent quota charging.
- [x] 1.3 Use native curl JSON and preserve transfer causes without relaxing sample safety gates.

## 2. Verification and release
- [x] 2.1 Run Go tests, race detection, vet, OpenWrt/UI/package fixtures, strict OpenSpec validation, and independent review.
- [x] 2.2 Build the SDK package from the exact reviewed head and verify its contents and digest.
- [x] 2.3 Stage rollback and provide commands before router tests; upgrade and inspect a regular quota-accounted probe and unchanged routing/settings.
- [x] 2.4 Publish and verify the GitHub change and release, recording device evidence.
