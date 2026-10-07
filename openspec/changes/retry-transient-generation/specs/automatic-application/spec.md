## ADDED Requirements

### Requirement: Recover from confirmed precommit changes
The controller SHALL defer application when a supported snapshot generation or apply opt-in changes before lease arm and rule commit. It SHALL retry through a fresh normal daemon tick without bypassing safety gates, and SHALL retain persistent blocking for unknown/conflicting/possibly committed failures.

#### Scenario: WAN changes during validation
- **WHEN** WAN state changes between the initial snapshot and the final precommit discovery
- **THEN** no stale rules are committed and no permanent application latch is set
- **AND** a later stable tick may apply freshly calculated weights after all ordinary validation checks

#### Scenario: Legacy transient latch is loaded
- **WHEN** state from v0.1.0-r2 contains an exact recognized precommit generation latch
- **THEN** the controller retires only that latch while preserving samples, schedule, budgets and opt-in
- **AND** application still requires validated current compatibility, watchdog readiness and policy ownership

#### Scenario: Possibly committed error
- **WHEN** an error occurs after lease arm or a policy ownership conflict is found
- **THEN** recovery and persistent fail-closed blocking remain in effect
