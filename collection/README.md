# Collection core

`collection` contains the in-process scheduler, collection runner, and asynchronous link-check queue. It deliberately has no PostgreSQL or HTTP dependency; application code supplies adapters for the narrow interfaces in `interfaces.go`.

## Integration

1. Implement `RunRepository` over the configured resource store.
2. Adapt live search with `LiveSearchFunc` or `AdaptCurrentSearch`, and expose each independently retried channel/plugin through a `SourceProvider`.
3. Optionally build a `LinkCheckQueue` with `LinkCheckRepository` and `LinkChecker` adapters, then pass it to `NewRunner`.
4. Call `Runner.Start` during service startup and `Runner.Stop` during shutdown.
5. Use `StartManual` for admin batches and `RecordExternal` (or `RecordExternalSource`) after a live external search.

The default schedule interval is 60 seconds. `MaxSourceRetries` is the number of retries after the first attempt, so its default value of two allows three attempts per source.

On startup, the runner resets abandoned `running` items and atomically claims existing `pending` items before new scheduled work. It drains recovered items in repository order without creating duplicate batches.

`RecordExternal` records an already-completed live search, so it does not take the internal collection batch guard and can run while a scheduled or manual batch is active. Its short persistence window is serialized with recovery and pending-item claims so the external item cannot be claimed by the internal runner.
