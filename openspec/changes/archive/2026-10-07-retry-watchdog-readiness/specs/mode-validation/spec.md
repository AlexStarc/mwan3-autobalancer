## ADDED Requirements

### Requirement: Keep mode validation current
The LuCI mode widget SHALL revalidate against each current readiness status without resetting values or writing configuration.

#### Scenario: Watchdog becomes ready after an inline refusal
- **WHEN** automatic mode has a cached validation error and a later report confirms readiness
- **THEN** cached validity, tooltip and invalid styling are updated to the valid state
- **AND** the selected mode and unsaved settings remain unchanged
