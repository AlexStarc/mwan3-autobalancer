# Retry transient generation changes

## Why

The controller permanently latches every Apply error, including a topology change detected before any rules are written. A Petra USB disconnect therefore leaves otherwise healthy WANs using stock weights indefinitely.

## What Changes

Classify confirmed precommit generation/opt-in changes as deferred application. Retry on the next normal daemon tick using a fresh snapshot and all existing safety gates. Keep conflicts, unsupported configuration, failed validation and any possibly committed transaction blocked. Migrate only exact legacy precommit generation latch messages, retaining samples, schedules, budgets and user preferences.

This changes controller behavior, not mwan3 traffic rules, UI layout or measurement frequency.
