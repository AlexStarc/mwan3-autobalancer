## Why

Cloudflare is unreliable on the user's mobile channels, while a fixed Yandex CDN URL has no permanent-address guarantee. Automatic discovery avoids routine manual URL replacement.

## What Changes

Add an optional Yandex provider with bounded per-WAN discovery, private runtime caching and failure-triggered refresh. Preserve manual URLs, traffic limits, sample confidence rules and independent recovery. Expose the choice in English and Russian LuCI.

## Capabilities

### New Capabilities
- `yandex-probe-discovery`: Automatic validated download targets, caching and recovery from endpoint changes.

### Modified Capabilities

## Impact

Go controller/configuration, LuCI, package versions, tests and documentation. No new Go dependencies or changes to stock mwan3 configuration.
