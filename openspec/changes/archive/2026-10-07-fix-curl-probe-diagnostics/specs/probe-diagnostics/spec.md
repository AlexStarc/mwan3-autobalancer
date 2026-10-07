## ADDED Requirements

### Requirement: Native curl statistics
The controller SHALL request native JSON statistics from curl so a transfer without an HTTP response remains parseable.

#### Scenario: No HTTP response
- **WHEN** curl fails before an HTTP response exists
- **THEN** its statistics SHALL contain a numeric zero HTTP code rather than invalid JSON containing `000`
- **AND** the probe SHALL report the original transfer failure

### Requirement: Preserve transfer causes
The controller SHALL preserve the runner error when statistics are malformed or absent, and SHALL still report invalid statistics if the runner succeeded.

#### Scenario: Failed command without valid statistics
- **WHEN** a failed command has no parseable final statistics
- **THEN** the returned error SHALL wrap the command failure and include the statistics error
- **AND** the probe SHALL produce no speed sample or additional retry
- **AND** the already reserved quota SHALL remain charged

### Requirement: Duration-capped samples remain bounded
The controller SHALL accept curl exit 28 as a rate sample only at the configured duration limit after successful HTTP body data flowed and all existing confidence and routing checks pass.

#### Scenario: Connection timeout at duration limit
- **WHEN** curl times out at the duration limit without HTTP 200/206 body bytes
- **THEN** the probe SHALL report the original timeout and produce no speed sample

#### Scenario: Valid duration-capped transfer
- **WHEN** curl times out at the duration limit after a valid HTTP 200/206 body transfer
- **THEN** the controller SHALL retain its existing validated speed calculation

#### Scenario: Completed successful transfer
- **WHEN** HTTP 200/206 succeeds with valid payload, duration, source, destination, and generation
- **THEN** the controller SHALL retain its existing validated speed calculation
