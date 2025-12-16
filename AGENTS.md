# Agents

This document helps AI agents work effectively in this codebase. It explains the philosophy, patterns, and pitfalls behind the code, so you can make good decisions on any task, not just scenarios explicitly covered. Apply these principles while reading the code for specifics.

## Philosophy

This is a minimal, idiomatic WebSocket library. Simplicity is a feature. Before adding code, consider whether it's necessary. Before adding a dependency, don't; tests requiring external packages are isolated in `internal/thirdparty`.

## Planning

When asked to plan, write to `PLAN.md`. Write for someone else, not yourself; don't skip context you already know. Research deeply before proposing solutions.

**Follow references.** If there's a link, issue, or RFC citation, read it. Document important findings in a research section so the implementer can verify.

**Focus on what and why.** Provide enough context for the implementer to understand the problem and constraints. Code examples can illustrate intent, but don't over-specify; leave room for the implementer to find a better approach.

**Tell them where to look.** Point to specific files, functions, or line numbers. Make claims verifiable.

**Review before finishing.** Make a final pass to check for gaps in research or unanswered questions, then update the document.

## Research

**Read the issue carefully.** Before designing a solution, verify you understand what's actually being requested. Restate the problem in your own words. A solution to the wrong problem wastes everyone's time.

**Understand the protocol first.** Code comments reference RFC 6455 (WebSocket), RFC 7692 (compression), and RFC 8441 (HTTP/2). When behavior seems odd, check the RFC section cited nearby.

**Trace from public API inward.** Start with `Accept` (server) or `Dial` (client), then follow to `Conn`, then to `Reader`/`Writer`. The internal structure follows these paths.

**Read tests for intent.** Tests often reveal why code exists. The autobahn-testsuite (`autobahn_test.go`) validates protocol compliance; if you're unsure whether behavior is correct, that's the authority.

**Check both platforms.** Native Go files have `//go:build !js`. WASM lives in `ws_js.go` (imports `syscall/js`). They have the same API but different implementations. WASM wraps browser APIs and cannot control framing, compression, or masking, so don't try to implement those features there.

**Search exhaustively when modifying patterns.** When changing how something is done in multiple places, grep for all instances. Missing one creates inconsistent behavior.

## Making Changes

**Every change needs a reason.** Don't reword comments, rename variables, or restructure code without justification. If you can't articulate why a change improves things, don't make it.

**Understand before changing.** Research the code you're modifying. Trace the call paths, read the tests, check both platforms. A change with good intentions but incomplete understanding can break things in ways you won't notice.

**Ask for clarification rather than assuming.** If requirements are ambiguous or you're unsure how something should work, stop and ask. A wrong assumption can waste more time than a quick question.

**Iterate, don't pivot.** When feedback identifies a problem, fix that problem. Don't discard everything and start over with a different approach. Preserve what's working; adjust what isn't.

**Verify examples work.** Trace through usage examples as if writing real code. If an example wouldn't compile, the design is wrong.

**Don't delete existing comments.** Comments that explain non-obvious behavior preserve important context about why code works a certain way. If a comment seems wrong, verify before removing.

**Check if it already exists.** Before proposing new API, read existing function signatures, return values, and doc comments. The feature might already be there.

**Ask: does this need to exist?** The library stays small by saying no. A feature that solves one user's problem but complicates the API for everyone is not worth it.

**Ask: is this the user's job or the library's job?** The library handles protocol correctness. Application-level concerns (reconnection, auth, message routing) belong in user code.

**Ask: what breaks if I'm wrong?** Context cancellation closes connections. Pooled objects must be returned. Locks must respect context. These invariants exist because violating them causes subtle bugs.

## Code Style

Follow Go conventions: [Effective Go](https://go.dev/doc/effective_go), [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments), and [Go Proverbs](https://go-proverbs.github.io/). Be concise, declarative, and factual.

Never use emdash. Use commas, semicolons, or separate sentences.

**Doc comments** start with the name and state what it does. Put useful information where users make decisions (usually the constructor, not methods).

**Inline comments** are terse. Prefer end-of-line when short enough.

**Explain why, not what.** The code shows what it does; comments should explain reasoning, non-obvious decisions, or edge cases.

**Wrap comments** at ~80 characters, continuing naturally at word boundaries. Don't put each sentence on its own line.

**Avoid:**

- Tautologies and redundant explanations the code already conveys
- Filler phrases: "Note that", "This is because", "It should be noted"
- Hedging: "basically", "actually", "really"

## Key Invariants

**Always read from connections.** Control frames (ping, pong, close) arrive on the read path. A connection that never reads will miss them and misbehave. `CloseRead` exists for write-only patterns.

**Pooled objects must be returned.** `flate.Writer` is ~1.2MB. Leaking them causes memory growth. Follow `get*()` / `put*()` patterns; return on all paths including errors.

**Locks must respect context.** The `mu` type in `conn.go` unblocks when context cancels or connection closes. Using `sync.Mutex` for user-facing operations would block forever on stuck connections.

**Reads and writes are independent.** They have separate locks (`readMu`, `writeFrameMu`) and can happen concurrently. Don't create unnecessary coupling between them.

**Masking is asymmetric.** Clients mask payloads; servers don't. The mask is applied into the bufio.Writer buffer, not in-place on source bytes.

## Testing

**Protocol compliance:** run autobahn tests with `AUTOBAHN=1 go test`. Some tests are intentionally skipped (UTF-8 validation adds overhead without value; `requestMaxWindowBits` is unimplemented due to `compress/flate` limitations).

**Frame-level correctness:** compare wire bytes. When two implementations disagree, capture the bytes and check against the RFC.

**WASM compatibility:** API changes need both implementations. Test that method signatures match.

**Test style:** prefer table-driven tests and simple want/got comparison.

## Commits and PRs

Use conventional commits: `type(scope): description`. Scope is optional but include it when changes are constrained to a single file or directory.

**Choose precise verbs:** `add` (new functionality), `fix` (broken behavior), `prevent` (undesirable state), `allow` (enable action), `handle` (edge cases), `improve` (performance/quality), `use` (change approach), `skip` (bypass), `remove` (delete), `rewrite` (substantial rework).

**Commit messages explain why.** State what was wrong with previous behavior, why it mattered, and why this solution was chosen. One to three sentences. Keep the tone neutral; don't editorialize.

**PR descriptions are for humans.** Don't fill out a template with "Summary", "Background", "Changes" headers. Don't restate the diff. Explain what reviewers can't see: the why, alternatives considered, and tradeoffs made. Show evidence (logs, benchmarks, screenshots). Be explicit about what the PR doesn't do.

**Link issues:** `Fixes #123` (PR resolves it), `Refs #123` (related), `Updates #123` (partial progress).

## RFC References

- RFC 6455: The WebSocket Protocol
- RFC 7692: Compression Extensions for WebSocket (permessage-deflate)
- RFC 8441: Bootstrapping WebSockets with HTTP/2

Section numbers in code comments refer to these RFCs.
