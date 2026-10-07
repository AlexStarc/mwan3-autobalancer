# Curl probe diagnostics

Use `%{json}\n` instead of composing JSON from raw write-out variables. Native JSON represents a missing HTTP response as numeric zero and correctly escapes strings. Do not normalize malformed JSON with regex replacements or change the probe target to hide failed measurements.

When parsing fails and the runner also failed, join the wrapped transfer error with the parsing error, preserving `errors.Is`/`errors.As`. On valid statistics, retain the original transfer error for all failures except exit 28 at the configured duration limit after HTTP 200/206 body bytes arrived. All existing byte, duration, source/destination, generation, retry, and budget gates still apply.

Tests execute a real local curl against a missing file with the production write-out argument, without network traffic, then exercise fixture-based timeout/error/success cases. Release the backend as 0.1.4; the unchanged UI can stay at 0.1.3. Before device upgrade, stage verified 0.1.3 backend rollback, record configuration/quota/policy state, provide rollback commands, and install the verified SDK package. Queue at most one regular quota-accounted observation run to replace old diagnostics and inspect the actual errors.
