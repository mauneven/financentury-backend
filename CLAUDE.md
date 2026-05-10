# CLAUDE.md — Financentury Backend

Guidance for any AI assistant operating in this Go (Fiber) + Postgres backend.

## Operating mode

You are authorized to act autonomously on **everything except git state**. You can:

- Read, edit, create, and delete source files.
- Run `go mod tidy`, `go build`, `go test ./...`, `go vet`.
- Start the server on `:8080` (configured via `PORT=8080` in `.env`).
- Modify handlers, middleware, routes, schema migrations, configuration.
- Run read queries against the local/dev Postgres for debugging.

## Git is user-only

**Only the user runs git.** Do NOT execute any of these without an explicit, in-message instruction that names the action:

- `git add`, `git commit`, `git commit --amend`
- `git push`, `git push --force`, `git push --force-with-lease`
- `git checkout -b`, `git branch`, `git branch -D`, `git switch -c`
- `git reset`, `git revert`, `git restore`, `git rm` (incl. `--cached`)
- `git tag`, `git stash`, `git merge`, `git rebase`, `git cherry-pick`
- `gh pr create`, `gh pr merge`, `gh release create`, `gh repo *`

Phrases like "validate", "make it run", "fix it", "improve the API" do NOT authorize git operations. Authorization requires explicit phrases like "commit this", "push it", "open a PR", "create a tag".

When unsure, stop and ask.

## Security

- `.env` is gitignored. It holds `DATABASE_URL`, `JWT_SECRET`, `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`. Never echo these to logs, commit them, or paste them into external tools.
- `JWT_SECRET` must stay ≥ 32 chars (config validates).
- Don't weaken existing security middleware:
  - `requestid` → `logger` → `recover` → `compress` → CORS ordering matters.
  - `Content-Security-Policy`, `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff` are deliberate.
  - `EnableTrustedProxyCheck` is gated on explicit `TRUSTED_PROXIES` — don't enable proxy header parsing globally.
  - `BodyLimit: 4MB` — bumping this needs a real reason.
- Don't run **destructive Postgres operations** against the remote database without confirming with the user. The migrations live in `migrations/`; production runs through Supabase MCP — confirm scope before applying.
- Don't bypass auth middleware on a route to "test" something — flag it instead.
- Token validation lives in `internal/middleware`. Treat it as load-bearing.

## Project conventions

- **Stack:** Fiber v2, pgx/v5 connection pool, golang-jwt/v5, lucide… (no, that's frontend). Pure REST, no GraphQL, no gRPC.
- **DB access:** all SQL goes through `internal/database/database.go`'s `Client` (Get/Post/Patch/Delete/RPC) or directly via `Pool.Query*`. The `Filter` builder is shared with the previous PostgREST shape — keep query strings parametrized.
- **Pool sizing:** `DB_MIN_CONNS=2`, `DB_MAX_CONNS=10` defaults are tuned to Supabase's pooler ceiling. Don't raise without checking.
- **Exec mode:** `pickQueryExecMode` picks `QueryExecModeExec` on the 6543 transaction pooler, `QueryExecModeCacheStatement` elsewhere. The transaction pooler doesn't support prepared statements — preserve this guard.
- **Cache headers:** `cacheControlAndETag` middleware adds `Cache-Control: private, max-age=10, stale-while-revalidate=30` + ETag for whitelisted GETs. Don't expand the whitelist to mutation-heavy paths.
- **Errors:** 5xx responses are masked to "internal server error"; the real error goes to logs with `request_id`.
- **WebSocket:** `internal/ws` runs a single Hub goroutine. Don't spin up parallel hubs.

## Useful commands

```bash
go mod tidy
go build .                                # produces ./backend (or pass -o)
go test ./...
PORT=8080 go run .                        # respects .env
curl http://localhost:8080/health
```

## House style

- No WHAT-comments. WHY only, when the reason is non-obvious. The existing codebase has detailed `// PERF:` / `// SECURITY:` comments — match that voice.
- No drive-by refactors of unrelated handlers when fixing a bug.
- New endpoints get tests in `*_test.go`.
- New schema changes go in `migrations/` (forward-only) and `schema.sql`.
