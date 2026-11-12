## ADDED Requirements

### Requirement: Reconnect-safe automatic measurement
Automatic payload measurements SHALL wait at least five continuously observed online minutes and fifteen minutes after the previous actual measurement on the same logical interface, across epoch changes and daemon restarts. Invalid samples SHALL NOT be reused after epoch changes.

#### Scenario: Flapping channel
- **WHEN** a measured WAN repeatedly disconnects and reconnects
- **THEN** each reconnect restarts settling while its attempt throttle survives
- **AND** other WAN schedules and routing supervision remain independent

### Requirement: Bounded failure retries
Actual failed measurements SHALL back off 15, 30, 60 minutes, capped at 60; successful measurements SHALL reset the streak. Successful calibration SHALL retain the configured ordinary interval. Calibration SHALL remain bounded to five actual attempts and a sufficient finite window. After exhaustion, on-change SHALL hold and hybrid SHALL wait its configured maintenance interval. Discovery deferrals SHALL NOT consume measurement attempts or failures.

#### Scenario: Restart during retry
- **WHEN** a daemon restarts before the persisted retry deadline
- **THEN** automatic probing waits for both the retry deadline and fresh online settling

### Requirement: Explicit manual semantics and truthful status
Manual probing SHALL bypass automatic timing guards while retaining quota and safety checks and recording actual attempts. Reported next_probe_at SHALL include automatic settling and throttle guards.

#### Scenario: Status during backoff
- **WHEN** an epoch changes during backoff
- **THEN** next_probe_at is no earlier than both settling completion and the persisted retry deadline
