# moolah — instructions for Claude

**This is a personal open-source project, not a Matav project.** The Matav-specific rules in the org-level CLAUDE.md (Jira tickets, `matav-server` schema files, `matav-mcp`, feature branches off `staging`) do not apply here. Committing directly on `main` is allowed.

moolah is a personal finance tool: a Go CLI plus a local htmx web UI over one SQLite file per "book". The design and its decisions are in `docs/design.md`. Read it before changing behavior.

## How this project is developed
The owner "vibe codes" it: Claude writes the code, and the owner checks behavior (CLI output, reports), not every line. So:
- `make check` must pass at the end of every task (gofmt, go vet, staticcheck, tests). Never finish a task with a failing check.
- Every invariant listed below has a test. A behavior change updates its test, and the README section that describes it.
- Keep changes small and phase-scoped. Prefer the simplest design that keeps the layer rules.

## Layer rules (do not break)
```
cmd/moolah  → cli
cli, web    → app            (never store or SQL directly)
app         → core, store, crypt, syncdir, (later: prices, importers)
syncdir     → crypt          (sync folder files only; no SQL)
store       → core           (the only package with SQL)
core        → nothing in this module (pure: no I/O, no SQL, no time.Now)
```

## Invariants (each has a test)
- Postings of a transaction balance to zero per currency, using cost (`@@`) when a posting has one.
- Amounts are exact decimals. Storage is integer minor units; an amount with more decimals than the currency allows is an error. Never use float64 for money.
- Real accounts (assets, liabilities) have one currency; a posting to a real account must be in that currency. Group accounts (no currency) cannot receive postings. Categories (income, expenses) and equity accept any currency.
- The account taxonomy is closed: an unknown name is an error with suggestions; nothing is created implicitly. Only `account add` / `category add` / `init` create accounts. Tags are the exception (open list).
- No posting after an account's close date.
- Exchange-rate lookup: latest rate on or before the date; source priority boi > ecb > manual > tx; inverse and cross rates (through ILS or USD) are allowed; a missing rate is an error, never 1:1.
- One user action = one DB transaction.

## Conventions
- Sign convention: amounts are from the account's point of view; negative = money leaves the account.
- Dates are `YYYY-MM-DD` strings in storage; `core.Date` in code.
- Errors shown to users are plain English and say how to fix the problem.
- Comments only when they say something the code cannot. Exported identifiers get doc comments. Test files: a one-line file comment, and one comment per `t.Run` stating the invariant.
- Golden files: `make golden` updates them. Review the diff before committing.
- Never commit real bank exports or real financial data. Test data is made up or anonymized.
