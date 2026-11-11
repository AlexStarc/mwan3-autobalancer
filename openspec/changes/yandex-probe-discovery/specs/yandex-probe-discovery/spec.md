## ADDED Requirements

### Requirement: Optional provider
The controller SHALL support `probe_provider=url` by default and `probe_provider=yandex`. Manual mode SHALL retain existing empty-URL and bounded probe behavior. Yandex mode SHALL work without a manual URL.

#### Scenario: Select automatic discovery
- **WHEN** Yandex is selected for an online WAN
- **THEN** the controller obtains a download target through that WAN and performs the existing bounded measurement.

### Requirement: Bounded validated discovery
Discovery SHALL bind to the WAN device/source, verify its route and generation, use HTTPS without redirects, cap time at eight seconds and metadata at 64 KiB, and reserve metadata traffic within the persistent per-WAN budget. Only validated HTTPS Yandex CDN large-download targets SHALL be accepted.

#### Scenario: Unsafe or malformed metadata
- **WHEN** discovery returns malformed, oversized or unsafe target data
- **THEN** it is rejected without a payload request to that target.

### Requirement: Independent cached targets
The controller SHALL cache targets privately per WAN device/source, refresh after 24 hours or three target failures, and limit discovery attempts to once per ten minutes. A failed refresh SHALL retain a usable cached target. Offline channels and status polling SHALL NOT trigger discovery.

#### Scenario: API temporarily unavailable
- **WHEN** refresh fails and a valid target is cached
- **THEN** the cached target remains usable under the normal measurement limits.

#### Scenario: Resolved URL changes
- **WHEN** discovery returns a different valid target for the same channel
- **THEN** measurement history and calibration progress are preserved.

#### Scenario: Discovery cooldown during calibration
- **WHEN** no target is available and discovery is in cooldown
- **THEN** the measurement is deferred until retry time without consuming a calibration attempt, including in on-change mode, while actual failed network attempts remain bounded by the existing five-attempt limit.

### Requirement: Minimal bilingual controls
LuCI SHALL expose manual and automatic Yandex modes in English and Russian, preserving unsaved settings during status refresh. Installation SHALL remain in observe mode with no enabled downloads.

#### Scenario: Existing installation upgrades
- **WHEN** a configuration without `probe_provider` is loaded
- **THEN** it continues to use the manual URL provider.
