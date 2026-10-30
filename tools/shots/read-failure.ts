/**
 * READ_FAILURE matches the tokens the surface logs when one of its reads fails, each of the form
 * `console.error("<name>NotRead", err)` — `inboxNotRead`, `runHistoryNotRead`, `rewriteNotRead` and
 * their siblings. A page that logged one is showing its error state, so its shot is refused: it
 * would record a fixture the app could not use rather than the surface.
 */
export const READ_FAILURE = /\b\w+NotRead\b/;
