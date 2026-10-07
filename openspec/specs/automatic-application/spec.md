# automatic-application Specification

## Purpose
Keep automatic WAN weighting available after confirmed changes detected before rules are written, while retaining policy ownership, opt-in, watchdog and uncertain-write recovery protections.

## Requirements

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

### Requirement: Wait for confirmed watchdog availability
The controller SHALL refuse rule writes while the independent watchdog is unavailable, and SHALL defer only explicitly classified read-only availability errors detected before lease writes to a later ordinary cycle. It SHALL preserve hard blocking for invalid identities, unknown failures and recovery or write failures.

#### Scenario: Ready record is temporarily absent
- **WHEN** the real watchdog readiness record is absent at a pre-arm check
- **THEN** no rules or lease are written and no permanent availability latch is set
- **AND** a later cycle can apply only after fresh genuine readiness and every normal safety check pass

#### Scenario: Wrong identity or recovery failure
- **WHEN** the recorded process is not the independent watchdog or a lease/recovery/write error occurs
- **THEN** the refusal remains hard-blocked and does not become a deferred availability retry
