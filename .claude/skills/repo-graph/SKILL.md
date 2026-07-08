---
name: repo-graph
description: Token-efficient repo navigation via a structural code graph. Use BEFORE any multi-file exploration of this codebase — questions like "how does X work", "where is Y defined/called", "what endpoints exist", "map the architecture", or before planning a change that spans packages. Replaces reading whole files with an AST-derived map, then reading only exact line ranges.
---

# Repo Graph — structural navigation for this monorepo

This repo ships `tools/repograph`, a stdlib-only Go CLI that AST-parses the
whole workspace on every invocation (<1s, never stale) and answers structural
queries. Use it instead of reading files to orient yourself; then Read only the
line ranges it reports.

## Workflow

1. **Start wide:** `go run ./tools/repograph shake` — modules, packages,
   one-line roles, LOC, endpoint counts. (~800 tokens vs ~65K for reading the
   repo.)
2. **Drill in** with exactly one of:

   | Command | Use when |
   |---|---|
   | `endpoints` | anything HTTP: what routes exist, which handler, where |
   | `package <dir-suffix>` | understanding one package: all symbols + signatures + line ranges |
   | `symbol <Name>` | locating a function/type: signature, doc, what it calls, who calls it |
   | `callers <Name>` | impact analysis before changing a function |
   | `file <basename>` | symbol layout of one file with line ranges |
   | `json` | full graph dump (rarely needed; large) |

3. **Read surgically:** every result carries `file:start-end`. Use the Read
   tool with `offset`/`limit` for exactly that range — do not read the whole
   file unless the range covers most of it.

## Rules

- From the repo root use `go run ./tools/repograph <cmd>`; from any subdir use
  `go run repograph <cmd>` (the module name resolves workspace-wide, and the
  tool walks up to `go.work` on its own).
- If a question is *structural* (where/what/who-calls), answer from graph
  output alone; only open source when behavior/logic matters.
- Grep is still right for *semantic* hunts ("anything retry-like",
  string literals, SQL). The graph knows structure, not meaning.
- After large refactors nothing needs rebuilding — every run re-parses.

## Known limits (do not fight them)

- Method-call edges resolve only when the method name is unique in the
  workspace. Interface dispatch (e.g. three `Verify` methods) yields no
  caller edges — fall back to `grep -rn "\.Verify(" --include="*.go"`.
- Only `.go` files (tests excluded). The DB schema lives in
  `DPDP/scripts/db/migrations/`; frontends (when they exist) are not indexed.
- Endpoint extraction understands Gin `Group`/verb literals; dynamically
  built routes would be missed.
