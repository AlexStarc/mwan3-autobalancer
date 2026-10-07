## ADDED Requirements

### Requirement: Wait for confirmed watchdog availability
The controller SHALL refuse rule writes while the independent watchdog is unavailable, and SHALL defer only explicitly classified read-only availability errors detected before lease writes to a later ordinary cycle. It SHALL preserve hard blocking for invalid identities, unknown failures and recovery or write failures.

#### Scenario: Ready record is temporarily absent
- **WHEN** the real watchdog readiness record is absent at a pre-arm check
- **THEN** no rules or lease are written and no permanent availability latch is set
- **AND** a later cycle can apply only after fresh genuine readiness and every normal safety check pass

#### Scenario: Wrong identity or recovery failure
- **WHEN** the recorded process is not the independent watchdog or a lease/recovery/write error occurs
- **THEN** the refusal remains hard-blocked and does not become a deferred availability retry
