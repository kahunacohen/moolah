# moolah

A personal finance tool for the command line, with an optional browser UI that runs on your own computer.

moolah tracks bank accounts, credit cards and retirement accounts in any currency, and reports everything in **one base currency that you choose**. Your data is one SQLite file on your computer. Nothing is sent anywhere, except requests for public exchange rates.

> **Status:** everything in this README works. Only one built-in import profile ships (`generic-csv`); for your bank, [write a profile](#import-profiles) in a few lines of TOML. Investment holdings and lots are not planned.

**This README explains ideas and workflows. For the exact commands and flags, use the built-in help:** `moolah -h` lists all commands, and `moolah <command> -h` (for example `moolah account -h` or `moolah account add -h`) shows the options and examples.

## Contents

1. [Install](#install)
2. [Quick start](#quick-start)
3. [Main ideas](#main-ideas)
4. [Accounts and categories](#accounts-and-categories)
5. [Recording transactions](#recording-transactions)
6. [Finding and changing transactions](#finding-and-changing-transactions)
7. [Importing bank and card files](#importing-bank-and-card-files)
8. [Pension and retirement accounts](#pension-and-retirement-accounts)
9. [Exchange rates](#exchange-rates)
10. [Reports](#reports)
11. [The browser UI](#the-browser-ui)
12. [Languages and numbers](#languages-and-numbers)
13. [Settings and files](#settings-and-files)
14. [Backups](#backups)
15. [History of changes](#history-of-changes)
16. [Exporting to text](#exporting-to-text)
17. [Several computers](#several-computers)
18. [Several books](#several-books)
19. [Security and privacy](#security-and-privacy)
20. [Upgrades](#upgrades)
21. [Troubleshooting](#troubleshooting)
22. [Development](#development)

## Install

Download the `moolah` binary for Linux (amd64 or arm64) from the project's GitHub **Releases** page, or [build it](#development), and put it in your `PATH`:

```sh
install -m 755 moolah ~/.local/bin/
moolah completion bash > ~/.local/share/bash-completion/completions/moolah   # or: completion zsh
```

Shell completion also completes your account and category names.

## Quick start

```sh
moolah init                      # asks for base currency, language, encryption key, sync folder
moolah account add Checking --kind bank --currency EUR --opening 25000 --date 2026-11-01
moolah account add Visa --kind card --currency EUR
moolah category list             # init created starter categories
moolah add visa -85 restaurants --payee "Corner Cafe"
moolah balance
moolah serve                     # the same book in your browser
```

## Main ideas

- **Accounts** are real places that hold money: bank accounts, cash, cards, loans, pension funds. Each account has **one currency**, the currency of its statement.
- **Categories** say where money comes from or goes to: `food`, `rent`, `salary`. A category can be inside another (`restaurants` in `food`). A category has no currency, so it collects spending in any currency.
- **Every transaction moves money from one place to another.** A grocery purchase moves money from your bank account to `food`. Paying your card moves money from the bank account to the card. moolah records both sides, so totals always add up. (Inside, this is double-entry bookkeeping. You don't need to know it.)
- **Signs are from the account's point of view:** negative means money leaves the account. A purchase on a card makes the card balance more negative; a negative card balance is money you owe.
- **Record amounts as the statement shows them**, in the account's currency. moolah never converts when you enter data.
- **Reports convert to your base currency.** Spending uses the rate of each transaction's date; balances use the rate of the report date. The difference is shown as a **currency effect** line.
- **The list of accounts and categories is closed.** You create them on purpose, and a typo is an error with suggestions, not a new category:
  ```
  error: unknown account "restaurnts". Did you mean restaurants?
  ```
- **Names are unique**, so a short name is always enough (`checking`, `restaurants`). Only when a spending and an income category share a name (`gifts`) do you write the full name: `expenses:gifts` or `income:gifts`.

## Accounts and categories

```sh
moolah account add Checking --kind bank --currency EUR --opening 25000 --date 2026-11-01
moolah account add "Visa Gold" --kind card --currency USD
moolah category add coffee --in food
moolah category add bonus --in salary --income
```

Both have `list`, `rename`, `merge` and `close` (see `moolah account -h` and `moolah category -h`).

- **Names:** upper and lower case do not matter. Names can have spaces (quote them in the shell), but not `:`, `#`, `=`, `;` or `|`.
- **An account's currency cannot change.** The **opening balance** is the balance on the day you start using moolah for the account.
- **Nothing with transactions is deleted.** `merge` moves all history into another account or category. `close` keeps the history but refuses new transactions after the close date.
- **Full names and moving.** moolah keeps a tree: `assets:bank:checking` is the account `checking` of kind `bank`. You see full names in exports and in CSV and JSON output. A full name in `rename` moves the item: `moolah category rename coffee drinks:coffee`.
- **Categories:** spending in `coffee` is included in the total of `food`. You can record directly on `food` if no subcategory fits. `uncategorized` holds imported rows that no rule matched.

**Kinds of account** group your accounts in lists and reports, and tell moolah whether the account is something you own (asset) or owe (liability). `init` creates:

| Kind | Type | For |
|---|---|---|
| `bank` | asset | checking and savings accounts |
| `cash` | asset | a wallet, cash at home |
| `investment` | asset | brokerage accounts, funds |
| `pension` | asset | pension funds, 401(k), IRA |
| `card` | liability | credit cards |
| `loan` | liability | loans, a mortgage |

Add your own with `moolah account kind add crypto --asset` (or `--liability`). A kind's name is unique too: while the kind `cash` exists, call the account `Wallet`.

## Recording transactions

```sh
moolah add checking -200 groceries --payee "Supermarket" --note "for the party"
moolah add checking 18500 salary --date 2026-11-01             # income is positive
moolah transfer checking visa 3200                              # pay the card
moolah add --payee "Super-Pharm" --posting checking=-300 --posting health=180 --posting household=120
```

- The date is today unless you give `--date`. The amount is in the account's currency.
- A transaction with more than two lines (`--posting`) must add up to zero in each currency.
- **Tags** group transactions across categories, for example a trip: `--tag vacation-2026`, then `moolah report income --tag vacation-2026`. Unlike categories, a tag is created when you first use it.

**Foreign currencies:** record what the statement shows, in the account's currency.

| Situation | What to enter |
|---|---|
| EUR purchase on a USD card | the **USD amount the card charged**: `moolah add chase -41.30 travel --note "38 EUR"` |
| Pay a USD card from a EUR account | both amounts: `moolah transfer checking chase 390 --to-amount 420` (390 € left, 420 $ arrived) |
| …with a bank fee | `moolah transfer checking chase 390 --to-amount 420 --fee 5` (the fee goes to `fees`) |

A transfer with two currencies keeps the rate you really got (390 ÷ 420). Reports can compare it with the official rate.

## Finding and changing transactions

```sh
moolah transactions                         # all accounts
moolah transactions checking                # one account, with running balance
moolah transactions --category food --from 2026-10-01
moolah transactions --search amazon         # payee or note contains "amazon" (any case)
moolah transactions visa --search 45.90     # a line with this amount (either sign)
moolah transactions edit 42                 # opens transaction 42 in $EDITOR
moolah transactions delete 42
moolah categorize 42 fees                   # change only the category
```

- The first column is the transaction ID that the other commands take.
- With one account, the list shows a running balance. A search hides rows, not money: the balance still counts every transaction.
- Without an account, each amount is seen from the transaction's own account: a purchase is negative, a refund positive. A transfer between your accounts shows its size without a sign.
- `edit` shows the transaction as text: date, payee, note, tags and one line per account. You can change anything, and also split it into more lines. If what you saved has an error, moolah shows it and opens the editor again, with your changes kept. If the transaction changed elsewhere in the meantime (for example in the browser), the save is refused instead of overwriting it.

## Importing bank and card files

Most data should come from your bank and card exports, not from typing.

```sh
moolah import ~/Downloads/visa-2026-10.xlsx --account visa
moolah categorize                 # give a category to the rows no rule matched
```

1. moolah reads CSV, XLSX, and HTML tables saved as .xls, in any language, with title rows and total rows.
2. It finds the file's format (its **profile**) from the headers, or you choose one with `--profile`. After the first import, `--account` is optional: moolah remembers the account for each profile.
3. It skips rows already imported, so overlapping periods are safe.
4. Your **rules** choose categories. Other rows go to `uncategorized`.
5. `moolah categorize` shows each uncategorized row. Type a category, or an account to make the row a transfer (`?` lists names, Enter skips, `q` stops). You can save the choice as a rule. moolah suggests a pattern without the codes that change every time (`AMAZON PRIME*N46RX2TU3` → `AMAZON PRIME`), and first shows the other rows it would match.

A profile can also fill the **note** from a column, such as a memo or address column.

**Rules** match the payee and note with a regular expression (not case-sensitive). The first match wins:

```sh
moolah rule add 'supermarket|grocer|lidl' groceries
moolah rule add 'shell|esso' fuel --max-amount 200
moolah rule add 'visa payment' visa                  # an account: the row becomes a transfer
```

**Payments between your own accounts** appear in both exports, but moolah records them once: the second file's row is linked to the transfer from the first file (same accounts and amount, at most 5 days apart). A transfer you entered yourself is found the same way.

When the two accounts have **different currencies**, each export has only one of the two amounts. The first file's row waits. When the other file is imported, moolah pairs them if exactly one waiting row fits the exchange rate (within 5%). Otherwise nothing is guessed: `moolah categorize` shows the waiting rows and asks for the amount that arrived (`moolah categorize transfer 57 1200`). The browser's review page does the same.

**Undo:** `moolah import list`, then `moolah import undo 17` removes everything import 17 created.

**Card details** are kept with each transaction: for a foreign purchase, the original amount and currency; for installments, "payment 3 of 12" (each monthly charge is imported as the card charged it).

### Import profiles

A profile is a small TOML file that describes one bank's export. No code change or rebuild is needed. Profiles are stored in the book, so backups and sync include them.

```sh
moolah profile show generic-csv > my-bank.toml   # start from a similar profile, then edit it
moolah profile add my-bank.toml                  # checks it and saves it in the book
moolah import test ~/Downloads/export.xls --profile my-bank --account checking   # imports nothing
moolah profile edit my-bank                      # fix it in $EDITOR
```

```toml
name = "my-bank"
encoding = "windows-1252"     # only for files that are not UTF-8

[detect]
headers = ["Booking date", "Description", "Debit", "Credit"]

[columns]
date = "Booking date"
date_format = "02.01.2006"    # Go layout: day.month.year
payee = "Description"
memo = "Reference"            # becomes the note
debit = "Debit"
credit = "Credit"
```

[docs/profiles.md](docs/profiles.md) lists every key, with a card example (pending rows, foreign purchases, installments).

## Pension and retirement accounts

For pension funds and retirement plans, you usually only know the total value from the statement. Record deposits as normal transactions, and enter the value when a statement arrives:

```sh
moolah add "pension fund" 2400 salary:pension --payee "Employer deposit"
moolah value "pension fund" 412300 --date 2026-10-31
```

`value` records only the difference from the balance, as gain or loss in the income category `gains`. So the gain for a period is the change in value minus deposits. For an account in another currency, reports show the market gain and the currency effect separately.

## Exchange rates

moolah needs daily rates to convert to your base currency. It downloads missing rates **automatically** when a report needs them, and stores them; after that, it works offline. Only currency codes and dates are sent.

Where rates come from, in this priority:
1. **Rate sources**: public APIs, each described in a TOML file. Built in: the European Central Bank (`ecb`, about 30 major currencies) and the Bank of Israel (`boi`, against the shekel). You can add your own: see [docs/rate-sources.md](docs/rate-sources.md). The `rate_sources` setting sets their order.
2. **Rates you enter**: `moolah rates add 2026-10-31 USD EUR 0.92`, or `moolah rates import FILE.csv`.
3. **Rates from your own two-currency transfers**, only when nothing else exists.

On a weekend or holiday, moolah uses the last rate before that date. If no direct rate exists, it goes through a shared currency. A missing rate is an error, never 1:1. See `moolah rates -h`.

## Reports

```sh
moolah balance                                   # every account, today
moolah balance --date 2026-06-30 --in USD
moolah report networth --from 2026-01-01
moolah report income --month 2026-10 --depth 1   # top-level categories only
moolah report income --month 2026-10 --by charge-date
moolah reconcile visa --balance -4210.50 --date 2026-10-31
```

- Amounts are in your base currency. `--in USD` uses another currency for one report.
- **Which rate:** spending and income use the rate on **each transaction's date**, so October's spending does not change later. Balances and net worth use the rate on the **report date** ("what is it worth now?"). Net worth shows the difference:
  ```
  Net worth 2026-01-01                    412,300 €
    + income                              231,000 €
    − spending                           -198,400 €
    + investment gains                     21,700 €
    + currency effect                      -6,150 €
  Net worth 2026-10-31                    460,450 €
  ```
- **Card purchases have two dates:** the purchase date and the charge date. Reports use the purchase date; `--by charge-date` groups by card bill.
- `reconcile` compares an account with your statement. If they differ, it lists the transactions near that date that may be missing or duplicated.
- **Output formats:** use `--format csv` or `--format json` for other tools. They always write numbers as `1234.56`. In CSV, an amount column becomes two columns (number and currency), and lists show full names (`assets:bank:leumi`). Group rows such as `assets:bank` are subtotals: filter them out before you add up a column.

## The browser UI

```sh
moolah serve
moolah running at http://127.0.0.1:8473/?token=…   (Ctrl+C to stop)
```

Your browser opens automatically. The browser UI has the transaction list (with search), adding and editing, review of imports, accounts, categories and reports. History is only on the command line. It uses the same data, so you can use the browser and the command line at the same time.

- It is only reachable from your own computer, and stops with Ctrl+C.
- **Port:** 8473, or the `serve_port` setting. If that port is busy (for example, another book is already served), moolah uses a free port and says so. `--port` uses exactly that port, or fails.
- **Bookmarks work.** The sign-in token stays the same between runs, so `http://127.0.0.1:8473/` and open tabs keep working after a restart. `moolah serve --new-token` replaces the token and signs out every browser.

## Languages and numbers

- Set the language and number format with `moolah config set locale de_DE` (or `en_US`, `he_IL`, …). `init` suggests one from your system.
- Output uses the language's separators: `1,234.56` in English, `1.234,56` in German. When you **type** an amount, always use a dot for decimals (`-85.50`).
- Payees, notes and names can be in any language.
- The browser UI is translated into English and Hebrew; other languages fall back to English. To add one, copy `web/i18n/en.json` to `web/i18n/<language>.json` and translate it. Right-to-left languages (Hebrew, Arabic, Persian, …) switch the page to right to left.
- Terminals show mixed right-to-left and left-to-right text badly. If a table looks wrong, use `--format csv` or the browser UI.

## Settings and files

**Everything about a book is stored in the book**, so it syncs and is in every backup. Only settings that belong to one computer are in `~/.config/moolah/config.toml`. `moolah config list` shows all settings and where each is stored; `moolah config set KEY VALUE` changes one.

| Setting | Stored in | Default | Meaning |
|---|---|---|---|
| `base_currency` | book | set by `init` | currency for all reports; changing it changes no stored data |
| `locale` | book | from your system | language and number format |
| `rate_sources` | book | all, those for your base currency first | exchange-rate sources to use, most trusted first, for example `ecb` |
| `backup_recipient` | book | none (a passphrase) | age or SSH **public** key that encrypts backups and sync snapshots |
| `backup_dir` | this computer | `~/.local/share/moolah/backups` | backup folder |
| `offline` | this computer | `false` | `true`: never download exchange rates |
| `serve_port` | this computer | `8473` | port of `moolah serve` (1024–65535) |

`config.toml` also holds the current book, each book's sync folder and key path, and the browser sign-in token. You can edit it by hand; an unknown key is an error. `config set` rewrites the file, so your comments are not kept.

| What | Location |
|---|---|
| Books, one file each | `~/.local/share/moolah/books/<name>.db` (plus `-wal` and `-shm` files while open) |
| Backups | `~/.local/share/moolah/backups/` |
| This computer's settings | `~/.config/moolah/config.toml` |
| moolah's private encryption key | `~/.config/moolah/age-key.txt` |
| This computer's name for sync | `~/.local/state/moolah/machine-id` |

Which book a command uses: `--db PATH`, then `MOOLAH_DB`, then `--book NAME`, then `MOOLAH_BOOK`, then the current book (`moolah book use`), then `main`.

## Backups

```sh
moolah backup                       # encrypted, into the backup folder
moolah backup --to /media/usb/
moolah restore moolah-2026-11-04T10-30.db.age          # asks before replacing the book
moolah restore FILE --db /tmp/test.db                  # restore elsewhere, to look at it
```

- Backups are encrypted with [age](https://age-encryption.org): with your `backup_recipient` key, or else with a passphrase. **If you lose the key or the passphrase, the backup cannot be opened.**
- A backup is a consistent copy, also while `moolah serve` runs.
- `restore` also accepts a plain book file (`.db`), for example a copy from another computer. moolah checks it first and refuses a damaged file or one that is not a moolah book.
- For scripts without a key, set `MOOLAH_PASSPHRASE`. To decrypt by hand: `age -d -i ~/.ssh/id_ed25519 FILE > moolah.db`.
- moolah does not delete old backups. Test a restore now and then.
- For a scheduled backup, use cron and a `backup_recipient` key: `0 3 * * 0  moolah backup >/dev/null`.

## History of changes

moolah keeps every change to your book: what changed, when, on which computer, and with which command. Changes made in the browser are kept too.

```
$ moolah history
ID  Time              Computer     Command                       Changes
 7  2026-11-04 12:00  laptop-1a2b  transactions delete 2 --yes   1 transaction deleted
 6  2026-11-04 11:58  laptop-1a2b  config set locale de_DE       1 setting changed

$ moolah history 7                   # exactly what action 7 changed
$ moolah history --tx 2              # every action that changed transaction 2
```

- One command, one import, or one change in the browser is one action. Changed fields show as `payee: Shufersal → Rami Levy`.
- Account and category names are shown as they are **today**.
- Everything you enter is kept: transactions, accounts, categories, tags, settings, rules, profiles and rate sources. Downloaded rates are not.
- History is in the book, so it syncs and is in backups. **Deleted data stays in history**; `moolah history prune --before 2026-01-01` really removes it.
- moolah shows history but does not undo it. Make the opposite change, or restore a backup.

## Exporting to text

`moolah export ~/moolah-export` writes the whole book as text files into a new or empty folder, so your data stays readable without moolah.

| File | Contents |
|---|---|
| `moolah.journal` | accounts, transactions and rates for [hledger](https://hledger.org) and [Ledger](https://ledger-cli.org) |
| `transactions.csv` | one row per transaction line, for spreadsheets and scripts |
| `prices.csv` | all exchange rates: `date,from,to,rate,source` |
| `rules.csv` | your import rules, in matching order |
| `settings.toml` | the book's settings |
| `profiles/*.toml`, `rate-sources/*.toml` | your own profiles and rate sources |

A journal transaction:

```
2026-11-01 (4) Transfer
    assets:bank:leumi       -1545.00 ILS
    liabilities:card:chase  420.00 USD @@ 1530.00 ILS
    expenses:fees           15.00 ILS
```

- `(4)` is the moolah ID, and `@@` is the total cost in another currency. A card charge date is a second date (`2026-10-03=2026-11-10`). A note follows the payee after ` | `. Tags and details are comment lines that start with `;`.
- `hledger -f moolah.journal balance` shows the same balances. For Beancount: `hledger -f moolah.journal print -O beancount`.
- In `transactions.csv`, `kind` is `normal`, `opening` or `valuation`. To total spending by category, filter `account` on `expenses:` and add up `amount` per currency.
- **The files are not encrypted.** History is not exported, and moolah cannot read an export back (use backup and restore, or sync).

## Several computers

moolah can use one book on several computers, through a folder that you already sync with Dropbox, Syncthing, Nextcloud or a similar tool. moolah puts only **encrypted snapshot files** in that folder, never the live database.

**The one rule: work on one computer at a time.** moolah does not merge changes from two computers. If both changed data, it stops and asks which copy to keep.

**First computer:** `moolah init` asks for a sync folder (or later: `moolah sync init ~/Dropbox/moolah`). Sync needs an encryption key: `init` creates `~/.config/moolah/age-key.txt`, or uses your SSH key. **Copy the private key to each computer** in a safe way, to the same path.

**Another computer:** install moolah, copy the key, and wait until the folder has synced. Then run `moolah init` and answer **yes** to "Do you already use moolah on another computer?" (without prompts: `moolah init --sync-dir ~/Dropbox/moolah --join`).

**Daily use** needs nothing extra. Before each command, moolah loads the newest snapshot; after each change, it writes a new one. `moolah serve` checks every 30 seconds, and saves a few seconds after a change and when it stops. `moolah sync status` shows the state, `moolah sync now` syncs at once, and `--no-sync` skips sync for one command.

**Switching computers:** stop `moolah serve` on the first one, wait a few seconds for your sync tool to upload, then continue. If you don't, you see `warning: moolah is in use on laptop since 10:32`.

**If both computers changed data,** every command stops until you choose:

```
error: conflict: laptop-3f2a and desktop-91c0 both changed data since snapshot 212.
  laptop-3f2a (this computer): 4 transactions that the other side does not have
  desktop-91c0: 38 transactions that the other side does not have
Choose the copy to keep: moolah sync resolve --keep laptop-3f2a | --keep desktop-91c0
```

The other copy is saved as a backup. Redo its changes. Imports are easy to repeat, because moolah skips rows it already has.

**How it works:** each computer keeps its full database, so moolah also works offline. Each change writes a new, encrypted, write-once file (`snap-<number>-<parent>-<computer>.age`) and renames it into place when it is complete, so a sync tool never sees a half-written file. The parent number tells moolah whether a newer snapshot continues your data (load it) or both sides changed (conflict). moolah keeps the last 20 snapshots plus one per day for 30 days, and each computer deletes only its own. A computer refuses snapshots of another book. The sync folder and key are set per computer, in `config.toml`. `moolah sync off` stops sync on one computer and keeps its data.

## Several books

A **book** is a separate set of finances, for example for a parent whose money you manage. Books share nothing: each has its own accounts, settings, rules, sync folder and backups. With one book, you can ignore this: the book is called `main`.

```sh
moolah book add mom              # asks the same questions as init
moolah book use mom              # every command now uses mom
moolah --book main balance       # one command on another book
MOOLAH_BOOK=mom moolah serve     # this terminal only
```

With more than one book, reports and the browser UI show the book name.

## Security and privacy

- **The database file is not encrypted.** Protect it with full-disk encryption. moolah creates it readable only by your user (`0600`).
- **Backups and sync snapshots are always encrypted.** Exports are not.
- **Never put the live database in a cloud-sync folder.** Sync tools can damage an open SQLite file. Use moolah's sync instead.
- **The browser UI** listens only on `127.0.0.1`. It is protected from other websites and local programs by a sign-in token (kept in `config.toml`, sent once in the URL, then kept in an HttpOnly cookie), a `Host` check (against DNS rebinding), and an `Origin` check with a CSRF token on every change.
- **Network:** moolah connects only to download exchange rates, only to the hosts of the rate sources you use (`ecb`: `api.frankfurter.dev`, `boi`: `edge.boi.gov.il`), and sends only currency codes and dates. There is no telemetry and no update check. `moolah config set offline true` or `--offline` turns the network off completely.
- moolah never asks for bank passwords.
- **Deleting a transaction does not remove it from the file:** history keeps it until `moolah history prune`.

## Upgrades

- Replace the binary. On the first run, moolah updates the book's schema automatically, in one transaction: if anything fails, the book stays as it was.
- **Before upgrading a book, moolah copies it** to the backup folder (`pre-migrate-v1-to-v2-<time>.db.age`, encrypted with `backup_recipient`). Without a key, the copy is a **plain** `.db` file; delete it when the new version works. To go back, install the older moolah and restore the copy.
- An older moolah refuses to open a newer book. **With sync, upgrade every computer:** a computer with an older moolah stops with "Upgrade moolah on this computer" and changes nothing.

## Troubleshooting

| Message or problem | Fix |
|---|---|
| `unknown account "X". Did you mean …?` | Use the suggestion, or create the name with `account add` or `category add`. |
| `"gifts" is ambiguous: expenses:gifts, income:gifts` | Write the full name, as shown. |
| `transaction does not balance: USD +12.00` | The lines don't add up to zero in that currency. |
| `no USD→EUR rate on or before 2026-09-14` | Connect to the network and run `moolah rates fetch`, or add the rate with `moolah rates add`. |
| `account old-card is closed since 2026-10-31` | The date is after the account's close date. |
| `no import profile matches this file` | [Write a profile](#import-profiles) for your bank. |
| An import gave wrong amounts or dates | `moolah import undo <id>`, fix the profile, and check it with `moolah import test` before you import again. |
| `conflict: … both changed data` | See [Several computers](#several-computers). Choose a copy with `moolah sync resolve`. |
| `changed elsewhere, reload` | The transaction changed after you opened it. Reload the page and edit again. |
| Browser: `not signed in` or `wrong token` | Open the address that `moolah serve` printed, with `?token=`. |
| Rates fail to download | `moolah rates source test <name> USD EUR` shows the error. Fix the source with `moolah rates source edit`, or enter rates yourself. |
| `database is locked` | Another long write (for example a big import) is running. Wait and try again. |
| Hebrew or Arabic looks reversed in the terminal | A terminal limitation. Use `--format csv` or the browser UI. |
| Is the book consistent? | `moolah check` checks balances, references, close dates and SQLite's integrity. |

You can read the database with `sqlite3 -readonly ~/.local/share/moolah/books/main.db`. Do not change it with SQL: that skips moolah's rules. (History still records such a change, but without a command.)

## Development

moolah runs on Linux (x86-64 or arm64); other systems are not tested. Building needs Go (the version in `go.mod`) and `make`, but no C compiler. The browser UI is embedded in the binary, so there is no frontend build.

```sh
make build        # → ./bin/moolah
make check        # gofmt, go vet, staticcheck, all tests; must pass before every commit
make golden       # update golden-file test output after an intended change; review the diff
make install      # copies the binary to ~/.local/bin
```

The code is written mostly with Claude Code. `CLAUDE.md` has the rules (layers, invariants, conventions), and `docs/design.md` has the design.

```
cmd/moolah  main
cli         command line (cobra)
web         browser UI (Go templates + htmx)
app         use cases; the CLI and the web UI call only this layer
importers   file formats, profiles, rules
prices      exchange-rate sources
syncdir     the sync folder's files
crypt       age encryption
store       SQLite; the only package with SQL
core        domain rules; no I/O
```

**A built-in import profile** goes in `importers/profiles/`, with an anonymized sample file and a test. Keep the structure, headers and number formats exactly, but remove names, account numbers and ID numbers. Never commit a real bank export.

## License

MIT. See [LICENSE](LICENSE).
