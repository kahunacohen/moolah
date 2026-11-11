# moolah design

> **Implementation notes (where the build differs from the plan below):**
> - **moolah is not tied to a country.** The plan below was written with an Israeli user in mind, so its examples use shekels, Israeli banks and Hebrew. In the build, no country, currency, bank or rate source is hard-coded: rate sources and import profiles are TOML data, locales come from CLDR, and `init` takes its defaults from the system locale.
> - Import profiles use flat `[columns]` keys (`amount`, `negate`, `debit`, `credit`, …) instead of inline tables. Reference: `docs/profiles.md`.
> - Every imported row becomes a transaction at once: in its rule's category, or in `expenses:uncategorized`. `review` then changes the category, so progress is always saved.
> - Old binary `.xls` files are not supported (CSV, XLSX and HTML-as-XLS are).
> - No starter rules for card companies: a rule needs the user's own card account. `rule add 'max' liabilities:card:max` does it in one line.
> - Settings: everything about a book is in its database. Only computer-specific settings (current book, sync folder and key path per book, `backup_dir`, `offline`) are in `~/.config/moolah/config.toml`. `moolah config` handles both and shows where each lives. `moolah sync keygen` creates an age key.
> - Transfer matching links same-currency rows only. A cross-currency transfer is recorded with `moolah transfer … --to-amount`.
> - Exchange-rate sources are TOML files (`docs/rate-sources.md`), not Go code. moolah fetches each currency into the report currency, from the first source that publishes the pair (or two legs through a pivot). The order comes from the `rate_sources` setting; `prices.source` has no CHECK constraint.
> - Only one built-in import profile (`generic-csv`) ships until real, anonymized bank/card exports are available.
> - Import guesses only ISO dates; other formats need `date_format`. Files that are not UTF-8 use the profile's `encoding` (default Windows-1252). The plan's "starter rules for Israeli card companies" were not built.

## Context
A personal money manager for one user who is comfortable on the command line.
- Simple. One user, local data, no server or user accounts.
- Core logic is separate from input and display. A CLI and a local browser UI use the same core.
- Many accounts (bank, credit card, pension, retirement) in different currencies.
- Reports use a **base currency** that the user chooses (for example ILS). It can change later without changing stored data.
- Hebrew/RTL text and locale number formats.

**How it is built:** the user is a programmer who wants good architecture but **does not want this to become a project for them**. Claude writes the code ("vibe coding"). The user checks behavior (CLI output, reports), not every line. So the design relies on: strict layer boundaries, a repo `CLAUDE.md` with the rules, strong automated tests as the guardrail, a single `make check` command, and small phases, each ending with a working binary.

`~/moolah` is empty, so there is no existing code to reuse.

## Decisions
| Topic | Decision |
|---|---|
| Language | Go |
| Persistence | SQLite, one file |
| Model | Double-entry internally; simple "account + category" input |
| Categories | Closed taxonomy; an unknown name is an **error only** (no prompt, no flag) |
| Conversion | Record native amounts; convert at report time |
| Investments | Level A (value only) for pension / Gemel / Hishtalmut and 401(k)/IRA; Levels B/C maybe later |
| Installments | Recorded as the card charges them; "payment N of M" kept as metadata |
| Import profiles | TOML only (no Go profiles); built-ins embedded in the binary, user profiles stored in the DB |
| Tags | In phase 1: labels across categories (`#vacation-2026`), `--tag` filter on reports, rules can add tags |
| Several sets of books | Named books, each one a separate DB file (see 8d); a current book is remembered, so `--book` is rarely needed |
| Distribution | Open source, MIT license. Release binaries built by GitHub Actions + GoReleaser |
| Report month for card purchases | Purchase date by default; `--by charge-date` to compare with card bills |
| Several computers | **Option B: automatic handoff** through a user-chosen sync folder (encrypted snapshots, fork detection, no merging). Real merging (event log) is out of scope. |
| Encryption | Disk encryption + DB mode 0600 + backups encrypted with age; DB-file encryption is optional later |
| Out of scope | Budgeting, recurring transactions / forecasting, tax reports, multiple users, bank scraping |

---

## 1. Core data model: double-entry ledger
- **Account**: hierarchical name (`assets:bank:leumi`, `liabilities:card:chase`, `expenses:food`). Categories are accounts under `income:` / `expenses:`.
  - **Real accounts** (assets, liabilities) have **one native currency**, the currency of the statement.
  - **Categories** (income, expenses) and equity have **no fixed currency**. `expenses:food` can receive ILS from Leumi and USD from the US card; reports convert to the base currency.
  - Storage: one `accounts` table `(id, parent_id, name, type, currency NULL, closed_on NULL)`. `name` is the short name only; the full path is built from the parents, so a rename is one row update. Subcategories (`food:restaurants`) are child rows, and totals roll up to the parents. A parent can also receive postings directly.
- **Transaction**: date, payee, note, metadata, **tags**, and 2 or more **postings**.
- **Tags**: free labels on a transaction (`vacation-2026`, `renovation`), stored in `tags` + `transaction_tags`. Unlike categories, tags are an open list: `--tag x` creates the tag if needed. Reports accept `--tag` to filter, and import rules can add tags.
- **Posting**: account + amount in **that account's currency**.
- **Balance rule**: postings balance to zero **in each currency**. A transaction with two currencies implies a rate, and that rate is saved as a price (source `tx`).
- **Amounts**: exact decimal (`shopspring/decimal` in the core; integer minor units in storage). Never floating point.
- **Commodity**: a currency or a security, using the same type.
- Input stays simple: `moolah add leumi -200 food` creates both postings.

## 2. Closed category taxonomy
- Accounts are created only with explicit commands. Postings refer to `accounts.id` through a FK.
  - `moolah account add assets:bank:leumi --currency ILS`: real accounts; `--currency` is required. Only `assets:` and `liabilities:` are allowed.
  - `moolah category add food:restaurants`: creates `expenses:food:restaurants`; `--income` creates under `income:` instead. No currency. `category list|rename|merge|close` show and accept names **without** the `expenses:`/`income:` prefix.
  - Both commands call the same `app` use case; the user never needs to know that categories are accounts. The web UI uses the same split: an "Accounts" page and a "Categories" page.
- 5 fixed roots (assets, liabilities, income, expenses, equity), enforced with `CHECK`. A parent must exist before its child.
- An unknown name is an error with "did you mean" suggestions. A short name (`food`) works only if exactly one leaf matches.
- `account rename` / `merge A into B` / `close --date`. An account that has postings cannot be deleted.
- `moolah init` seeds a small starter tree. Import rules may name only existing accounts; rows with no match go to `expenses:uncategorized` → `moolah review`.

## 3. Entering foreign-currency data
**Record what the statement shows, in the account's currency. Do not convert at entry.**

| Case | Postings |
|---|---|
| USD purchase on a US card | `card:chase -42.17 USD` / `expenses:shopping 42.17 USD` |
| EUR purchase on a US card | the USD amount charged; `orig: 38.00 EUR` as metadata |
| Foreign purchase on an Israeli card | the ILS amount charged; original amount and currency as metadata |
| Pay a USD card from ILS | `leumi -1530 ILS` / `card:chase +420 USD` → rate 3.643 saved as a price |
| Transfer with a fee | 3 postings, the fee goes to `expenses:fees` |

## 4. Conversion at report time
- Price table `(date, commodity, quote, rate, source)`. Lookup = latest rate on or before the date. Sources: Bank of Israel, ECB, rates implied by transactions, manual entry.
- **Historical** valuation (income/expense reports): the rate on each posting's date.
- **Current** valuation (balances, net worth): the rate on the report date.
- The net-worth report shows an **FX gain/loss** line, so the two modes reconcile.
- Base currency is a setting; `--in USD` overrides it for one report. A missing rate is an error that names the currency and date, never a silent 1:1.
- **Where rates come from** (`prices/` package, one `Fetcher` interface):
  - **Bank of Israel** representative rates (שער יציג): the official X→ILS rates, published each business day, with a free public API and full history. Main source for ILS pairs. History: the SDMX series `RER_<CUR>_ILS` with `format=csv` and start/end period parameters (XML is the default without it). Current day: the JSON `PublicApi/GetExchangeRates` endpoint. Public sources disagree on the exact SDMX path (`…/sdmx/v2/data/dataflow/BOI.STATISTICS/EXR/1.0/…` vs `…/ws/public/sdmxapi/rest/data/…`), so in phase 2 test against the live API and record sample responses as test fixtures.
  - **ECB** euro reference rates, through the free Frankfurter API (no key, history from 1999). Used for pairs that BoI does not cover, and for cross rates.
  - **Manual**: `moolah rates add 2026-10-31 USD ILS 3.65`, or `moolah rates import rates.csv`.
  - **Implied by transactions**: saved automatically (fallback only).
- **When rates are fetched**:
  - `moolah rates fetch`: gets missing days, from the earliest transaction date to today, only for currencies that the user's accounts and postings use.
  - **Automatically** when a report needs a rate that is missing (only public data is sent: currency codes and dates). `--offline` turns this off; without network, a report fails with a clear "missing rate" error.
  - Rates are stored in SQLite, so after the first fetch everything works offline. A rate that was fetched is not fetched again.
- **Rate lookup details**: one daily rate per pair; weekends and holidays use the last earlier business day. Store one direction and invert when needed. If a direct pair is missing, use a cross rate through ILS or USD (EUR→USD = EUR→ILS ÷ USD→ILS).
- **Source priority**: official rates (BoI, ECB) first, manual next, rates implied by transactions only as a fallback. Transaction rates are still kept, so a report can show "the rate you got vs the official rate" (the bank's hidden conversion fee).
- **Rounding**: full precision while calculating. Historical mode converts each posting and then sums; current mode sums per currency and then converts once. Round to the currency's minor unit only for display.

## 5. Value-only investment accounts (Level A)
- Contributions are normal transactions (`income:salary:employer-pension → assets:invest:pension`).
- `moolah value pension 412300 --date 2026-10-31` books only the difference: `assets:invest:pension +Δ` / `income:invest:gains -Δ`.
- A USD 401(k) in an ILS report shows the market gain and the FX effect separately (from section 4).

## 6. Import pipeline
```
file ─reader─> grid ─profile─> []NormalizedRow ─dedup─> staging ─rules─> review ─> transactions
```
- **Reader** (format only): CSV with encoding detection (UTF-8, BOM, Windows-1255), XLSX, old XLS, and HTML-as-XLS. Removes bidi marks, trims cells.
- **Profile** (one per bank/card format): **declarative TOML only** (user decision; no Go profiles). One generic engine in `importers/` reads a profile and converts a grid to `[]NormalizedRow`.
  - Profile keys: `name`, `detect` (header names, optional filename pattern), `[table]` (`header_row = "auto"|N`, `skip_if`, `stop_at`), `[columns]` (`date`, `charge_date` with Go date layouts; `payee`; `memo`; `external_id`; `amount` as one column with `negate`, or `{debit, credit}`; `currency` / `orig_amount` / `orig_currency` with value `map`; `installment` with a regex that has 2 groups), and `[defaults]` (for example a fixed currency).
  - Storage (user decision): built-in profiles are embedded in the binary (`importers/profiles/*.toml`). **User profiles are stored in the DB** (`import_profiles` table: name, TOML text, timestamps), so backups include them. Commands: `profile add <file.toml>`, `profile edit <name>` (opens `$EDITOR`; on a validation error it shows the error and reopens, like `crontab -e`), `profile list`, `profile show <name>` (built-in or user; used as a starting point), `profile export <name>`, `profile remove <name>`. A user profile with the same name overrides a built-in one.
  - Strict validation **on save** (and on load for built-ins, in tests): unknown keys, missing required columns and bad regexes are errors that name the key.
  - Profile ↔ account mapping is personal, so it is stored separately (`profile_accounts` table): after the first `import --account`, moolah remembers the account for that profile. If a profile is mapped to more than one account, `--account` is required.
  - `moolah import test <file> [--profile P]`: dry run; prints the detected profile and the normalized rows, writes nothing. This is how a user checks a new profile.
  - A file that the TOML cannot describe means **the engine gets a new generic option** (code change in the engine, with a test), not a special-case profile in Go.
  - Readers (CSV/XLSX/XLS/HTML-as-XLS, encodings) stay in Go; they are about file formats, not banks.
- **NormalizedRow**: `Date, ChargeDate, Amount, Currency, OrigAmount, OrigCurrency, Payee, Memo, ExternalID, Installment{N,Of}, RawHash`.
- **Dedup**: the bank's ID if it has one; otherwise `(account, date, amount, payee, occurrence index on that day)`.
- **Staging**: an `import_rows` table (raw row, normalized row, batch ID) → `moolah import undo <batch>`.
- **Rules**: ordered (payee regex, amount range) → existing account.
- **Review**: `moolah review` (CLI), later a web screen.
- The first profiles are chosen in the import phase, based on the export files the user has then.
- **Transfers between own accounts (avoid counting twice):**
  - Rules may point to **any** account, not only categories. A rule like `'max|מקס' → liabilities:card:max` turns the monthly card debit in the bank export into a **transfer**, not an expense. The purchases are already counted from the card export.
  - **Transfer matching**: when an imported row becomes a transfer to another own account, moolah looks for the same transfer already imported from the other side (same amount in that account's currency, date within ±5 days, not yet matched). If found, it links them as one transaction instead of creating a second one. With two currencies (ILS out, USD in), a match is only suggested, and the user confirms it in `review`.
  - The starter rules include the debit names of the common Israeli card companies.
- **Pending rows** (for example `עסקאות בקליטה`): **skipped** through the profile's `skip_if`, because their amount or date can still change and would break duplicate detection. They are imported later, when they are final.

## 7. Architecture
```
cmd/moolah/   main
core/         pure domain: types, balance rule, valuation, reports. No I/O.
store/        Store interface + SQLite. The only package that opens the DB.
prices/       rate fetchers (BoI, ECB) behind one interface
importers/    readers, TOML profile engine + built-in profiles, NormalizedRow, dedup, rules
sync/         snapshots in the sync folder: pull, push, fork detection, locks
app/          use cases: AddTransaction, Import, Review, Balance, Report, Value, FetchRates, Backup
cli/          cobra commands → app
web/          htmx server-rendered UI → app (no separate JSON API)
```
Rules (written in the repo `CLAUDE.md`): `core` imports nothing from the project; `cli` and `web` call only `app`; only `store` contains SQL.

## 8. Tech stack
- Go, one static binary. `moolah serve` listens on `127.0.0.1` only (random free port by default, `--port` to fix it), prints the URL and opens the browser. It runs in the foreground; Ctrl+C stops it. Local security: a random session token (from the URL, stored in an HttpOnly cookie), a `Host` header check (blocks DNS rebinding), and an `Origin` check plus CSRF token on POST (blocks other websites from sending requests to localhost).
- SQLite driver: `ncruces/go-sqlite3` (pure Go; has encrypting VFS for later). **Check its maturity in phase 0**; fallback `modernc.org/sqlite`. WAL mode, so the CLI and the web server can run at the same time.
- `cobra`, `html/template` + htmx, `embed.FS`, `golang.org/x/text`, `filippo.io/age`, `excelize` for XLSX.
- Data at `~/.local/share/moolah/moolah.db` (mode 0600). `moolah backup` = `VACUUM INTO` + age encryption.

## 8a. Settings, CLI conventions (decided while drafting the README)
- **Settings live in the DB** (`settings` table), so a backup contains everything: `moolah config set base_currency ILS`, `locale he_IL`, `offline true`. No separate config file. DB path: `--db` flag > `MOOLAH_DB` env var > default path.
- **Sign convention**: amounts are from the account's point of view. Negative = money leaves the account (a purchase on a card makes the card balance more negative = you owe more).
- **Commands**: `init --base X`, `account add|list|rename|merge|close` (`--opening AMOUNT --date D` for the opening balance → `equity:opening`), `category add|list|rename|merge|close`, `add <account> <amount> <category> [--date] [--payee] [--note]` (date defaults to today), `transfer <from> <to> <amount> [--to-amount] [--fee] [--date]`, `add --posting acct=amount …` for any multi-line transaction, `import <file> --account A [--profile P]`, `import list|undo`, `rule add|list|remove`, `review`, `value`, `rates fetch|list|add|import`, `rates source list|show|add|edit|remove|test`, `balance`, `register`, `report networth|income`, `reconcile`, `serve`, `backup`, `restore`, `config get|set|list`, `check` (ledger integrity check), `edit <id>` (opens `$EDITOR`), `delete <id>`, `profile add|edit|list|show|export|remove`, `import test`, `completion`. Global flags: `--db`, `--offline`, `--in`, `--format table|csv|json`, `--quiet`, `-v`.
- **Backups**: `backup_recipient` setting (age or SSH public key) allows unattended backups; without it, a passphrase is asked. Schema migrations run automatically on start, after an automatic pre-migration backup; an older binary refuses a newer schema.

## 8b. Using moolah on several computers (Option B: automatic handoff)
The user moves between computers often, but works on one at a time.
- Each machine has its own **local** live DB. The user sets `sync_dir` (a folder that Dropbox / Syncthing / Google Drive / Nextcloud already syncs). Only **encrypted, write-once snapshot files** go there, never the live DB.
- **Lineage**: each DB has `db_id` (UUID, the same on all machines for one set of books), `machine_id` (per machine, stored outside the synced data, at `~/.local/state/moolah/machine-id`), and a `change_seq` that a trigger increases on every write transaction. Each snapshot records `seq`, `parent_seq`, `machine` and a content hash. File name: `snap-<seq>-<machine>.age`.
- **Pull** (before every command, and when `serve` starts and every N seconds): find the newest snapshot. If local has no changes since its last synced snapshot and the remote snapshot descends from it → replace the local DB (fast-forward). If both sides changed since the common snapshot → **fork**: stop with a clear message; nothing is changed.
- **Push** (after every command that wrote data; `serve` pushes after changes, with a short delay): `VACUUM INTO` → age encrypt → write to a temp name → rename. Writing a new file every time means the sync tool never sees two machines changing the same file.
- **Resolve**: `moolah sync resolve --keep local|remote` → the other copy is saved as a backup, and moolah shows a summary of what it had (manual transactions, imports, category changes) so the user can redo or import it again.
- **Lock / presence**: `lock-<machine>.json` with a heartbeat while moolah is running (always for `serve`; for the CLI only during the command). Another machine that sees a fresh lock warns: "in use on laptop since 10:32". This is a warning, not a hard lock, because sync tools have delays.
- **Cleanup**: keep the last N snapshots (default 20) plus one per day for 30 days; older files are deleted by the machine that created them.
- **Keys**: `backup_recipient` / identity as for backups (age key or SSH key). Each machine needs the key to decrypt.
- Commands: `sync init <dir>` (first machine: push the first snapshot), `sync join <dir>` (another machine: pull and adopt `db_id`), `sync status`, `sync now`, `sync resolve`, `sync off`. `--no-sync` skips sync for one command.
- **Setup through `init`**: `moolah init` asks interactively for base currency, locale, backup key, and **sync folder (optional, Enter to skip)**. If the given folder already has snapshots, `init` offers to **join** them instead of creating new books (same as `sync join`). Flags for scripts: `--base`, `--locale`, `--sync-dir`, `--no-sync`, `--join`.
- **Unpushed changes**: the DB stores `last_pushed_seq`. If a push fails (folder missing, disk full) or the process was killed, the local data is still safe; the next moolah run pushes first (with the normal fork check). `sync status` shows "N changes not pushed yet".
- Package: `sync/`, which uses only `store` (snapshot / replace) and `age`.

## 8d. Named books
- A **book** = one complete, separate set of finances = one DB file: `~/.local/share/moolah/books/<name>.db`. Each book has its own accounts, settings, base currency, rules, profiles, sync folder and backup key. Books share nothing.
- `moolah book list|add <name>|rename|remove`, and **`moolah book use <name>`**, which sets the **current book** (saved in `~/.config/moolah/current-book`, similar to `kubectl config use-context`). Every command uses the current book, so `--book` is rarely needed.
- Order: `--book NAME` flag > `MOOLAH_BOOK` env var (for one shell) > current book > `main`. `--db PATH` still works for any file.
- With only one book, books are invisible: no flag and no name in the output. With two or more books, report headers and the web page header show the book name, so it is always clear which book you are looking at.
- `init` creates the first book, named `main`. `moolah serve --book mom` serves one book; the page header shows the book name.
- Implementation: only path resolution in `cli`/`web` startup. `core`, `app` and `store` are unchanged.

## 8c. Saving: CLI and web
- **One user action = one `app` use case = one DB transaction.** This is the same for a CLI command and a web form submit. A failure or Ctrl+C rolls back the transaction; nothing is ever half saved.
- **Long tasks** (import review): each decision is saved immediately in the staging table, so closing the browser or the terminal loses nothing; `review` continues where it stopped. The final commit creates all transactions in one DB transaction.
- **Edits from two places** (CLI and browser, or two browser tabs): `transactions.version` increases on every change. An edit form sends the version it loaded; if it changed, the save is rejected with "changed elsewhere, reload" (optimistic locking). Nothing is overwritten silently.
- **Text typed in a browser form but not submitted** is lost if the tab is closed. This is accepted (forms are short).
- **`serve` and sync**: push a few seconds after the last change (debounce) and on shutdown (Ctrl+C / SIGTERM). Pull every `sync_interval` when there are no local changes; the DB is replaced between requests, under a lock. When a pull is blocked by local changes that were not pushed, push first. `core` and `app` do not know about sync, except for one hook that runs pull/push around commands.

## 9. Languages and RTL
- UTF-8 everywhere. Web: `dir="rtl"` from the locale, CSS logical properties, numbers in `<bdi>`. CLI: LTR tables, plus `--format json|csv`.

## 10. Phases (each ends with a working binary and a commit)
0. **Setup**: `git init`, `go mod`, `LICENSE` (MIT), GitHub Actions for `make check` + GoReleaser for Linux amd64/arm64 release binaries, `README.md` (from Appendix A; update it in each phase when behavior changes), repo `CLAUDE.md` (architecture rules, invariants, "not a Matav project"), `Makefile` with `make check` (gofmt, `go vet`, staticcheck, tests), SQLite driver check.
1. **Core + CLI**: init with starter tree, named books, accounts and categories, tags, `add`, `transfer`, `balance`, `register`, `value`, manual prices, `--in` conversion, `config`, `backup`/`restore`, `check`.
2. **Sync between computers** (section 8b). Early, because the user moves between computers often.
3. **Rates and valuation**: BoI/ECB fetching, historical vs current valuation, FX line, net-worth and income/expense reports.
4. **Import**: readers, the TOML profile engine and first built-in profiles, `profile` commands, `import test`, dedup, staging, rules, review, reconcile.
5. **Web UI**: htmx pages for register, add/edit, review, reports, accounts, categories; RTL and locale.
6. (Optional) Investments Levels B/C.

## Verification
- `make check` must pass at the end of every task. This is the main guardrail for vibe-coded changes.
- `core` unit tests for the invariants: per-currency balance, rate lookup on or before the date, historical vs current valuation, the FX line reconciles totals, a valuation entry books only the difference after contributions, unknown accounts are rejected.
- Golden-file tests for CLI report output and for each import profile (anonymized samples only).
- Saving tests: an edit with an old version is rejected; an interrupted import review continues where it stopped; `init` with a sync folder that has snapshots joins them.
- Sync tests with two temp "machines" and one temp sync folder: fast-forward, push after a write, fork detected and nothing changed, resolve keeps the chosen side and backs up the other, a fresh lock gives a warning, and a half-written snapshot (temp name) is ignored.
- End-to-end script per phase: build, create a temp DB, load the sample transactions from section 3, run `moolah balance --in ILS` and `--in USD`, and compare with values calculated by hand. The user checks this output, not the code.

---

