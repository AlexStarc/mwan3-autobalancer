## Context

Yandex Internetometer requests `/internet/api/v0/get-probes` and obtains CDN download URLs chosen for the client. This API is undocumented and may change. Discovery must use each WAN's source and must not make normal status polling download anything.

## Decisions

- Keep manual URL mode as the default; opt into `yandex` explicitly.
- Use existing curl runner with source binding, route preflight, TLS validation, no redirects, at most 64 KiB metadata and an eight-second timeout. Reserve that maximum in the persistent WAN budget before requesting it.
- Store the metadata body in a private temporary file and remove it after the attempt. Native mwan3 emits diagnostics on stdout, including newlines embedded in curl arguments; parse curl statistics separately from the API body.
- Accept only HTTPS download URLs under the `.cdn.yandex.net` DNS boundary, without credentials, fragments or nonstandard ports; use the actual large download probe, not latency/upload/small timeout objects.
- Cache per interface/device/source in the private runtime directory. Refresh after 24 hours or three target transport/HTTP failures; limit discovery retries to once per ten minutes. Preserve cached URL if refresh fails, and never reset speed history just because a resolved target changes.
- A cooldown defers the next attempt without consuming calibration attempts. Extend calibration deadlines to permit the retry, while retaining the existing five actual failed-network-attempt bound, including on-change mode.
- Retain all payload limits, source and generation checks, confidence checks, scheduler behavior and independent watchdog protections. Discovery is read-only with respect to routing.

## Risks

The API can itself change; fail with a specific discovery error when no valid cached target is available. USB disconnects and operator outages remain separate failures. A 50 MiB object may be too short for high-speed links; existing confidence rejection still applies.

## Rollout

Release version 0.1.5 after local and SDK checks and independent reviews. Stage old packages and UCI backup, provide rollback instructions before router installation, then enable the provider and validate native probes on all online WANs.
