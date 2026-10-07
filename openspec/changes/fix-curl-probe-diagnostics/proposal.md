# Preserve curl probe failures

## Why

The hand-built curl write-out JSON embeds `http_code` as an unquoted number. Curl emits `000` when no HTTP response arrives, which is invalid JSON and masks the underlying transport error.

## What Changes

- Use curl's native JSON write-out, supported by the existing curl >= 8.4 gate.
- Preserve the command error when statistics are malformed or absent.
- Accept a duration-capped timeout only when a successful HTTP body actually flowed.
- Add regression coverage for real curl failures, timeout samples, and budget accounting.

## Impact

Only probe formatting, failure diagnostics, and release packaging change. Routing, automatic application, persistent quotas, measurement URL, and modem settings retain their existing contracts.
