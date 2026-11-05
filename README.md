# moolah

A personal finance tool for the command line, with an optional browser UI that runs on your own computer.

> **Status:** in development. This README describes the planned behavior.
> **Works now:** books, accounts and categories, tags, `add`, `transfer`, `edit`, `delete`, `register`, `balance` (with `--in`), `value`, exchange rates (`price`, `rates fetch`, automatic download), `report networth`, `report income`, `config`, encrypted `backup`/`restore`, `check`, sync between computers (`sync`).
> **Also works:** import (CSV, XLSX, HTML-as-XLS) with TOML profiles, duplicate detection, rules, `review`, transfer matching, `import undo`, `reconcile`.
> **Not built yet:** the browser UI (`serve`). Only one built-in profile ships (`generic-csv`); profiles for Israeli banks and cards will be added from real (anonymized) export files.

moolah keeps track of your bank accounts, credit cards and retirement accounts in any currency, and reports everything in **one base currency that you choose** (for example Israeli shekels). Your data is one SQLite file on your computer. Nothing is sent anywhere, except requests for public exchange rates.

## Contents

**End-user guide**
1. [Main ideas](#main-ideas)
2. [Install](#install)
3. [Quick start](#quick-start)
4. [Accounts](#accounts)
5. [Categories](#categories)
6. [Recording transactions](#recording-transactions)
7. [Foreign currencies](#foreign-currencies)
8. [Importing bank and card files](#importing-bank-and-card-files)
9. [Pension and retirement accounts](#pension-and-retirement-accounts)
10. [Exchange rates](#exchange-rates)
11. [Reports](#reports)
12. [Reconciling with a statement](#reconciling-with-a-statement)
13. [The browser UI](#the-browser-ui)
14. [Hebrew and right-to-left text](#hebrew-and-right-to-left-text)
15. [Backups](#backups)
16. [Working on different machines](#working-on-different-machines)
17. [Several sets of books](#several-sets-of-books)
18. [Common errors](#common-errors)

**Operations**
19. [Requirements and building](#requirements-and-building)
20. [Files and locations](#files-and-locations)
21. [Settings](#settings)
22. [Security model](#security-model)
23. [Network access](#network-access)
24. [Backup and restore in detail](#backup-and-restore-in-detail)
25. [How sync works](#how-sync-works)
26. [Upgrades and database migrations](#upgrades-and-database-migrations)
27. [Scheduled tasks](#scheduled-tasks)
28. [Data integrity and direct SQL access](#data-integrity-and-direct-sql-access)
29. [Troubleshooting](#troubleshooting)
30. [Development](#development)
31. [License](#license)

---

# End-user guide

## Main ideas

**Accounts** are real places that hold money: bank accounts, cash, credit cards, loans, pension funds. Each account has **one currency**, the currency of its statement. A US credit card is a USD account. A Leumi checking account is an ILS account.

**Categories** describe where money comes from or goes to: `food`, `rent`, `salary`. Categories can have subcategories (`food:restaurants`). A category has no currency. It can collect spending in any currency.

**Every transaction moves money from one place to another.** Buying groceries moves money from your bank account to the `food` category. Paying your credit card moves money from your bank account to the card. moolah records both sides, so totals always add up. (Inside, moolah uses double-entry bookkeeping. You don't need to know it to use moolah.)

**Record amounts as your statement shows them.** A 42.17 $ purchase on a US card is recorded as 42.17 $. moolah never converts at entry.

**Reports convert to your base currency.** Spending reports use the exchange rate of each transaction's date. Balance and net-worth reports use the rate of the report date. The difference is shown as a separate **currency effect** line, so you can see how much exchange rate changes gained or lost you.

**The list of accounts and categories is closed.** You create them on purpose. A typo is an error, not a new category.

## Install

Download the `moolah` binary for Linux (amd64 or arm64) from the project's GitHub **Releases** page, or build it from source (see [Requirements and building](#requirements-and-building)), and put it in your `PATH`:

```sh
install -m 755 moolah ~/.local/bin/
moolah --version
```

Shell completion (also completes your account and category names):

```sh
moolah completion bash > ~/.local/share/bash-completion/completions/moolah   # bash
moolah completion zsh  > "${fpath[1]}/_moolah"                               # zsh
```

## Quick start

```sh
# 1. Create your data file. moolah asks for your base currency, language,
#    encryption key and (optionally) a sync folder for other machines.
moolah init

# 2. Add your real accounts with their opening balances.
moolah account add assets:bank:leumi   --currency ILS --opening 25000 --date 2026-11-01
moolah account add liabilities:card:max --currency ILS
moolah account add liabilities:card:chase --currency USD

# 3. moolah init created starter categories. Look at them, and add your own.
moolah category list
moolah category add food:coffee

# 4. Record a purchase.
moolah add card:max -85 restaurants --payee "Cafe Landwer"

# 5. See your balances in shekels.
moolah balance
```

## Accounts

An account name has a type, then one or more levels, separated by `:`.

| Type | Use for | Examples |
|---|---|---|
| `assets` | things you own | `assets:bank:leumi`, `assets:cash`, `assets:invest:pension` |
| `liabilities` | money you owe | `liabilities:card:max`, `liabilities:mortgage` |

```sh
moolah account add assets:bank:leumi --currency ILS
moolah account add assets:bank:leumi --currency ILS --opening 25000 --date 2026-11-01
moolah account list
moolah account rename assets:bank:leumi assets:bank:leumi-checking
moolah account merge assets:cash:wallet assets:cash      # moves all history, then removes the first
moolah account close liabilities:card:old --date 2026-10-31
```

- `--currency` cannot change later. An account **without** `--currency` is a **group** that only holds other accounts (like `assets:bank`, which `moolah init` creates). A group cannot receive transactions.
- The **opening balance** is the balance on the day you start using moolah for this account.
- A parent level must exist before a child. `moolah init` creates the top levels.
- You cannot delete an account that has transactions. Merge it into another account, or close it. A closed account keeps its history but accepts no new transactions after the close date.
- **Short names** work everywhere when they are unique: `leumi` means `assets:bank:leumi`.

**Sign of balances:** a negative balance on a card or loan means you owe money. A purchase on a card makes the card balance more negative.

## Categories

Categories are for spending (`expenses`) and earnings (`income`). You write them without the type prefix.

```sh
moolah category list                       # tree of all categories
moolah category add food                   # spending category
moolah category add food:coffee            # subcategory of food
moolah category add salary --income        # income category
moolah category rename food:restuarants food:restaurants
moolah category merge snacks food          # moves all history from snacks to food
moolah category close hobbies:golf --date 2026-12-31
```

- A subcategory's spending is included in its parent's total. Reports can show only the top level (`--depth 1`).
- You can record directly on a parent (`food`) if no subcategory fits.
- A name that doesn't exist is an **error** with suggestions:
  ```
  error: unknown category "restaurnts". Did you mean food:restaurants?
  ```
  To add a new category, use `moolah category add`.
- `uncategorized` exists by default. Imported rows that no rule matches go there until you review them.

## Recording transactions

**A purchase or a payment from one account:**

```sh
moolah add <account> <amount> <category> [--date YYYY-MM-DD] [--payee TEXT] [--note TEXT]

moolah add leumi -200 food:groceries --payee "Shufersal"
moolah add card:max -85 restaurants --date 2026-11-02
moolah add leumi 18500 salary --payee "Employer Ltd"        # income: positive amount
```

The date is today if you don't give one. The amount is in the account's currency, so you don't write a currency.

**A transfer between two of your accounts** (including paying a credit card):

```sh
moolah transfer leumi card:max 3200                        # pay the Max card from Leumi
moolah transfer leumi assets:bank:savings 5000 --date 2026-11-10
```

**A transaction with more than two lines** (for example, a split purchase):

```sh
moolah add --payee "Super-Pharm" \
  --posting leumi=-300 \
  --posting health=180 \
  --posting household=120
```

The lines must add up to zero in each currency, or moolah rejects the transaction.

**Tags** group transactions across categories, for example everything for one trip:

```sh
moolah add card:max -1840 travel:flights --tag vacation-2026
moolah add card:chase -96 restaurants --tag vacation-2026
moolah report income --tag vacation-2026        # what did the trip cost?
moolah tag list
```

Unlike categories, a new tag is created when you first use it.

**See and change transactions:**

```sh
moolah register leumi                    # transactions of one account, with running balance
moolah register --category food --from 2026-10-01
moolah edit <transaction-id>             # opens the transaction in $EDITOR
moolah delete <transaction-id>
```

## Foreign currencies

**Rule: record what the statement shows, in the account's currency.**

| Situation | What to enter |
|---|---|
| USD purchase on a US card | `moolah add card:chase -42.17 shopping` (USD, because the account is USD) |
| EUR purchase on a US card | the **USD amount the card charged**: `moolah add card:chase -41.30 travel --note "38 EUR"` |
| Foreign purchase on an Israeli card | the **ILS amount the card charged**. Import stores the original amount for you. |
| Pay a USD card from an ILS account | `moolah transfer leumi card:chase 1530 --to-amount 420` (1,530 ₪ left Leumi; the card got 420 $) |
| A transfer with a bank fee | `moolah transfer leumi card:chase 1530 --to-amount 420 --fee 15` (fee goes to `fees`) |

When a transfer has two currencies, moolah saves the rate you actually got (here 1530 ÷ 420 = 3.643). Reports can compare it with the official rate, which shows how much the bank charged for the conversion.

## Importing bank and card files

Most data should come from your bank and card exports, not from typing.

```sh
moolah import ~/Downloads/max-2026-10.xlsx --account card:max
```

1. **moolah reads the file.** CSV (UTF-8 or Windows-1255), XLSX and XLS are supported, including Israeli exports with Hebrew headers, title rows and total rows.
2. **It detects the bank format** automatically. To choose a format yourself, use `--profile <name>`. `moolah profile list` lists the supported formats. After the first import, `--account` is optional: moolah remembers which account each format belongs to.
3. **It skips rows you already imported.** You can import overlapping periods safely.
4. **It applies your rules** to choose a category. Rows with no matching rule go to `uncategorized`.
5. **You review** the result:
   ```sh
   moolah review          # shows each uncategorized row, and you choose a category
   ```

**Rules** choose a category from the payee text (a regular expression, not case-sensitive):

```sh
moolah rule add 'shufersal|שופרסל|rami levy|רמי לוי' food:groceries
moolah rule add 'netflix|spotify' subscriptions
moolah rule add 'paz|דלק|sonol' car:fuel --max-amount 500
moolah rule list
moolah rule remove 3
```

Rules are checked in order. The first match wins. During `moolah review`, you can save your choice as a new rule.

**Undo an import:**

```sh
moolah import list            # earlier imports with their IDs
moolah import undo 17         # removes everything import 17 created
```

**Installments** (`תשלומים`): each monthly charge is imported as the card charged it, for example 100 ₪ per month for a 1,200 ₪ purchase in 12 payments. moolah keeps "payment 3 of 12" with the transaction.

**Foreign purchases on Israeli cards:** the ILS amount charged is recorded. The original amount and currency are kept with the transaction.

**Your bank's format is not supported?** Write a small TOML profile for it. No programming or rebuild is needed. See [Adding an import profile](#adding-an-import-profile).

## Pension and retirement accounts

For pension funds, Keren Hishtalmut, Kupat Gemel, 401(k) and IRA accounts, you usually only know the total value from the statement. moolah tracks the value and calculates the gain.

```sh
moolah account add assets:invest:pension     --currency ILS --opening 380000 --date 2026-01-01
moolah account add assets:invest:401k        --currency USD --opening 52000  --date 2026-01-01

# Deposits are normal transactions, so they are not counted as gains:
moolah add assets:invest:pension 2400 salary:pension --date 2026-02-10 --payee "Employer deposit"

# When a statement arrives, enter the value:
moolah value pension 412300 --date 2026-10-31
```

`moolah value` compares the value with the account balance and records only the difference as investment gain or loss (`income:invest:gains`). So:

> gain for a period = change in value − deposits

For a USD account like a 401(k), reports in shekels show the market gain and the currency effect separately.

## Exchange rates

moolah needs daily exchange rates to convert to your base currency. It gets them from:

1. **Bank of Israel** official representative rates (שער יציג), for all currencies against the shekel.
2. **European Central Bank** reference rates, for other currency pairs.
3. **Rates you enter yourself.**
4. **Rates from your own transactions** (only when no other rate exists).

moolah downloads missing rates **automatically** when a report needs them, and stores them. After that, everything works offline. Only currency codes and dates are sent, never your data.

```sh
moolah rates fetch                              # get all missing rates now
moolah price add 2026-10-31 USD ILS 3.65        # enter a rate yourself
moolah price import my-rates.csv                # columns: date,from,to,rate
moolah --offline report networth                # never use the network
```

On a weekend or holiday, moolah uses the rate of the last business day before it.

## Reports

All reports show amounts in your base currency. Add `--in USD` (or any currency) to use a different currency for one report.

```sh
moolah balance                                  # balance of every account, today
moolah balance --date 2026-06-30
moolah balance --in USD

moolah report networth                          # assets − liabilities, with currency effect
moolah report networth --from 2026-01-01 --to 2026-10-31

moolah report income --month 2026-10            # income and spending for a month
moolah report income --from 2026-01-01 --depth 1    # top-level categories only

moolah register card:chase --from 2026-10-01    # list of transactions

moolah report income --month 2026-10 --by charge-date   # card purchases by billing month
```

**Card purchases have two dates:** the purchase date (in the shop) and the charge date (when the card company takes the money from your bank). Reports use the **purchase date**. Use `--by charge-date` to see spending by card bill, for example to compare with "the October bill".

**Which exchange rate does a report use?**

- **Spending and income** (`report income`, `register`): the rate on the **date of each transaction**. What you spent in October stays the same, even when exchange rates change later.
- **Balances and net worth** (`balance`, `report networth`): the rate on the **report date**. It answers "what is this worth now?"

The net-worth report shows how the difference happened:

```
Net worth 2026-01-01                    412,300 ₪
  + income                              231,000 ₪
  − spending                           -198,400 ₪
  + investment gains                     21,700 ₪
  + currency effect                      -6,150 ₪
Net worth 2026-10-31                    460,450 ₪
```

**Output formats:** `--format table` (default), `--format csv`, `--format json`. Use CSV or JSON to process the data with other tools.

## Reconciling with a statement

Check that moolah agrees with your bank or card statement:

```sh
moolah reconcile card:max --balance -4210.50 --date 2026-10-31
```

If the balances are different, moolah lists the transactions near that date that may be missing or duplicated.

## The browser UI

```sh
moolah serve
moolah running at http://127.0.0.1:41873/?token=…   (Ctrl+C to stop)
```

Your browser opens automatically. The browser UI has the same features as the command line: transactions, adding and editing, review of imports, accounts, categories and reports. It uses the same data, so you can use the command line and the browser at the same time.

- The server is only reachable from your own computer.
- `--port 8080` uses a fixed port. `--no-browser` doesn't open a browser.
- The server stops when you press Ctrl+C or close the terminal.

## Hebrew and right-to-left text

- Payees, notes and category names can be in Hebrew or any language.
- Set the interface language: `moolah config set locale he_IL` (Hebrew, right-to-left) or `en_US`. Numbers and dates use the format of the locale.
- The browser UI changes to right-to-left for Hebrew.
- Terminals display mixed Hebrew and English text badly. If table output looks wrong, use `--format csv` or the browser UI.

## Backups

```sh
moolah backup                         # encrypted backup to the default backup folder
moolah backup --to /media/usb/        # somewhere else
moolah restore moolah-2026-11-04.db.age
```

Backups are encrypted. You choose a passphrase (or an SSH key) the first time. **If you lose the passphrase, the backup cannot be opened.** See [Backup and restore in detail](#backup-and-restore-in-detail).

## Working on different machines

You can use moolah on several computers, for example a desktop at home and a laptop. moolah moves your data between them automatically, through a folder that you already sync with Dropbox, Syncthing, Google Drive, Nextcloud or a similar tool.

**The one rule: work on one computer at a time.** moolah does not merge changes made on two computers at the same time. If that happens, it stops and asks you which copy to keep. It never mixes them silently.

### What moves between machines

| Moves automatically | Set up once on each machine |
|---|---|
| all transactions, accounts and categories | the `moolah` binary |
| settings (base currency, language, …) | the private key that decrypts the data (`~/.config/moolah/age-key.txt`, or your SSH key) |
| import rules and your import profiles | — |
| exchange rates already downloaded | — |

### Setting up the first machine

`moolah init` first asks for a sync folder. Give one now, or add it later with `moolah sync init <folder>`.

```
$ moolah init
Sync folder for your other computers (Enter to skip): ~/Dropbox/moolah
Base currency [ILS]: ILS
Language (en_US, he_IL) [en_US]: he_IL
Encryption key for backups and sync: 'new' (create one), 'ssh' (~/.ssh/id_ed25519.pub), a public key, or 'none' (a passphrase each time) [new]:
Created the encryption key ~/.config/moolah/age-key.txt. Keep a copy somewhere safe; copy it to your other computers.
This book syncs through ~/Dropbox/moolah.
```

Sync needs an encryption key. **Copy the private key file to each of your computers** in a safe way (for example with `scp`), to the same path. Without it, a computer cannot read the synced data. `moolah sync keygen` creates the key later if you skipped it.

### Adding another machine

Install moolah, copy the private key to this machine, wait until the sync folder is synced, then run `init` with the same folder. moolah sees the existing data and joins it, so it asks nothing else:

```
$ moolah init
Sync folder for your other computers (Enter to skip): ~/Dropbox/moolah
This folder has moolah data (last change: laptop-3f2a, 2026-11-04 10:32). Join it? [Y/n]:
Joined the book in the sync folder. Your data is up to date.
```

Without prompts: `moolah init --sync-dir ~/Dropbox/moolah --join`.

### Daily use

There is nothing extra to do.
- Before each command, moolah loads the newest data from the sync folder.
- After each change, moolah writes the new data to the sync folder.
- `moolah serve` checks the sync folder regularly, and saves your changes a few seconds after you make them and when you stop it.

`moolah sync status` shows the state:
```
Folder:   /home/you/Dropbox/moolah
Computer: desktop-91c0
State:    up to date (snapshot 214 from laptop-3f2a, 2 minutes ago)
```

`moolah sync now` loads and uploads immediately. `--no-sync` skips sync for one command.

### Switching machines

1. Finish on the first machine. Stop `moolah serve` if it is running (Ctrl+C).
2. Wait until your sync tool has uploaded the files (usually a few seconds).
3. Continue on the other machine.

If you skip step 1 or 2, you will probably see:
```
warning: moolah is in use on laptop since 10:32. Changes made here may conflict.
```
Go back and close moolah there, or wait for your sync tool.

### If both machines changed data

```
error: conflict: laptop-3f2a and desktop-91c0 both changed data since snapshot 212.
  laptop-3f2a (this computer): 4 transactions that the other side does not have
  desktop-91c0: 38 transactions that the other side does not have
Choose the copy to keep: moolah sync resolve --keep laptop-3f2a | --keep desktop-91c0
The other copy is saved as a backup.
```
Every command stops with this message until you choose. Nothing changes before that.
- The copy you don't keep is saved as a backup, so nothing is deleted.
- Then redo the changes from the other copy. **Imports are easy to repeat:** import the same file again, and moolah skips rows it already has.
- Usually, keep the side with the most manual work, and repeat the imports.

### Working offline

Each machine has its full data locally, so moolah works without a network. Your changes are sent when the sync tool is online again. `moolah sync status` shows `3 changes not uploaded yet` until then. Don't switch machines while one of them still has changes that are not uploaded.

### Moving to a new computer without sync

```sh
old$ moolah backup --to /media/usb/
new$ moolah restore /media/usb/moolah-2026-11-04T10-30.db.age
```

### Removing a machine or turning sync off

- `moolah sync off` stops sync on this machine. Its local data stays.
- To stop using an old machine, run `moolah sync off` there, or just stop using it. Its old snapshots are deleted automatically over time.

**Do not put the live database file itself in a sync folder.** moolah's sync puts only encrypted snapshot files there, which is safe. More details: [How sync works](#how-sync-works).

## Several sets of books

A **book** is one complete, separate set of finances. Use a separate book when you manage money for someone else, for example a parent. Books share nothing: each one has its own accounts, settings, base currency, rules, sync folder and backups.

If you have only one book, you can ignore this section. Your first book is called `main`, and moolah uses it automatically.

```sh
moolah book add mom                      # create a new book (asks the same questions as init)
moolah book list                         # * main, mom   (* = current book)
moolah book use mom                      # switch: every command now uses mom
moolah balance                           # mom's balances
moolah book use main                     # switch back
```

moolah remembers the current book, so you don't need to name it in every command. For a single command on another book, use `--book`:

```sh
moolah --book mom balance                # one command on mom; the current book does not change
MOOLAH_BOOK=mom moolah serve             # this terminal only
```

When you have more than one book, reports and the browser UI show the book name at the top.

## Common errors

| Message | Meaning and fix |
|---|---|
| `unknown account "X". Did you mean …?` | The name doesn't exist. Use the suggestion, or create it with `account add` / `category add`. |
| `"food" is ambiguous: expenses:food, income:food` | A short name matches more than one. Write more of the name. |
| `transaction does not balance: USD +12.00` | The lines don't add up to zero in that currency. Check the amounts. |
| `no USD→ILS rate on or before 2026-09-14` | No rate is stored and moolah couldn't download one. Connect to the network and run `moolah rates fetch`, or add the rate with `moolah price add`. |
| `account card:old is closed since 2026-10-31` | The transaction date is after the close date. |
| `conflict: laptop and desktop both changed data` | See [Working on different machines](#working-on-different-machines). Choose a copy with `moolah sync resolve`. |
| `changed elsewhere, reload` (browser) | The transaction was changed in another tab or by a CLI command after you opened it. Reload the page and edit again. |
| `no import profile matches this file` | Your bank's format is not supported yet. See [Adding an import profile](#adding-an-import-profile). |

---

# Operations

## Requirements and building

- Linux (x86-64 or arm64). Other systems may work but are not tested.
- To build: Go (version in `go.mod`) and `make`. No C compiler is needed (no CGO).

```sh
git clone <repo> && cd moolah
make build        # → ./bin/moolah, one static binary
make check        # format check, go vet, staticcheck, all tests
make install      # copies the binary to ~/.local/bin
```

The browser UI files are embedded in the binary. There is no Node.js or frontend build step.

## Files and locations

| What | Default location | Change it with |
|---|---|---|
| Books (one file per book, with all its data and settings) | `~/.local/share/moolah/books/<name>.db` | `--book NAME` / `MOOLAH_BOOK`, or `--db PATH` / `MOOLAH_DB` for any file |
| SQLite side files while running | `moolah.db-wal`, `moolah.db-shm` next to the DB | — |
| Backups | `~/.local/share/moolah/backups/` | `moolah backup --to DIR` |

Order of precedence for the DB path: `--db` flag, then `MOOLAH_DB`, then the default. To keep separate sets of books (for example for a family member), use separate DB files.

## Settings

Settings are stored in the database, so a backup includes them.

```sh
moolah config list
moolah config get base_currency
moolah config set base_currency USD
```

| Setting | Default | Meaning |
|---|---|---|
| `base_currency` | set by `init --base` | currency for all reports |
| `locale` | `en_US` | language, number format and text direction (`he_IL`, `en_US`) |
| `offline` | `false` | `true`: never download exchange rates |
| `rate_sources` | `boi,ecb` | order of exchange-rate sources |
| `backup_dir` | see above | default backup folder |
| `backup_recipient` | none | age public key or SSH public key for backups; none means a passphrase is used |

Changing `base_currency` changes no stored data. Only reports change.

## Security model

- **Your data is not encrypted inside the database file.** Protect it with **full-disk encryption** (for example LUKS). moolah creates the database with permissions `0600` (only your user can read it).
- **Backups are always encrypted** with [age](https://age-encryption.org).
- **Do not put the live database in a cloud-sync folder** (Dropbox, Google Drive, and so on). Sync tools can damage an open SQLite file, and the file is not encrypted. Use moolah's own sync (`moolah sync init`), which puts only encrypted snapshots in the folder.
- **The browser UI** listens only on `127.0.0.1`. It is protected against other websites and local programs by a random session token, a `Host` header check (against DNS rebinding) and an `Origin` check with CSRF tokens on every change.
- moolah never asks for or stores bank passwords.
- An encrypted database file may be added in a later version.

## Network access

moolah connects to the network only to download exchange rates:

| Host | When | Data sent |
|---|---|---|
| Bank of Israel (`edge.boi.gov.il`) | a report needs a missing rate, or `moolah rates fetch` | currency codes and date ranges |
| Frankfurter / ECB (`api.frankfurter.dev`) | the same, for pairs that BoI does not publish | currency codes and date ranges |

`moolah config set offline true` or `--offline` turns this off completely. Nothing else is sent: no telemetry, no update checks.

## Backup and restore in detail

`moolah backup` makes a consistent copy of the open database (SQLite `VACUUM INTO`), then encrypts it with age. It is safe to run while `moolah serve` is running.

```sh
moolah backup                                   # → backups/moolah-2026-11-04T10-30.db.age
moolah config set backup_recipient "$(cat ~/.ssh/id_ed25519.pub)"   # use an SSH key, no passphrase
moolah restore backups/moolah-2026-11-04T10-30.db.age               # asks before replacing the current DB
moolah restore FILE --db /tmp/test.db           # restore to another file, to look at it
```

- Without `backup_recipient`, every backup asks for a passphrase. For scripts, set the `MOOLAH_PASSPHRASE` environment variable instead.
- You can also decrypt a backup by hand with the `age` tool: `age -d -i ~/.ssh/id_ed25519 FILE > moolah.db`.
- **Test a restore** now and then, to a temporary file.
- moolah does not delete old backups. Remove them yourself, or with a scheduled task.

## How sync works

- Each computer keeps its own local database. The sync folder contains only encrypted, write-once files:
  ```
  ~/Dropbox/moolah/
    snap-214-laptop.age      # full encrypted copy of the data, snapshot 214
    snap-213-desktop.age
    lock-laptop.json         # "in use on laptop", with a heartbeat
  ```
- Every snapshot records which snapshot it was based on. This is how moolah knows whether a newer snapshot continues your local data (load it) or both sides changed (conflict).
- A new file is written for every change and renamed into place only when complete, so a sync tool never sees a half-written or shared file.
- moolah keeps the last 20 snapshots and one per day for 30 days, and deletes older ones. Each computer deletes only its own files.
- Each book has a hidden ID. A computer refuses to load a snapshot of a different book.
- The computer's name comes from `~/.local/state/moolah/machine-id`. It is not inside the synced data.
- Every computer needs the private key that matches `backup_recipient`, to decrypt snapshots.
- `--no-sync` skips sync for one command. `moolah sync off` stops sync on this computer.
- The sync folder and key path are set **per computer** (they can differ between computers), in `~/.config/moolah/sync/<book-id>.json`. They are not book settings.
- The decryption key is `~/.config/moolah/age-key.txt` if it exists, else `~/.ssh/id_ed25519`. Use `sync init --identity <file>` for another key.
- `moolah serve` checks the sync folder every 30 seconds.

## Upgrades and database migrations

- Replace the binary. On the first run, moolah updates the database schema automatically if needed.
- **Before migrating, moolah makes an automatic backup** (`backups/pre-migrate-<version>.db.age`).
- An older binary refuses to open a database from a newer version, with a clear error.
- `moolah --version` shows the binary version and the database schema version.

## Scheduled tasks

None are required. Optional examples:

```sh
# crontab -e
0 9 * * *  moolah rates fetch --quiet                 # rates every morning
0 3 * * 0  moolah backup --quiet                      # backup every Sunday (needs backup_recipient)
```

A systemd user timer works the same way. A scheduled backup needs `backup_recipient`, because it cannot ask for a passphrase.

## Data integrity and direct SQL access

```sh
moolah check
```

checks that every transaction balances, every account reference is valid, there are no postings after an account's close date, and SQLite's own integrity check passes.

The database is a normal SQLite file, so you can query it directly:

```sh
sqlite3 -readonly ~/.local/share/moolah/moolah.db
```

Use read-only mode. Changing data with SQL skips moolah's rules. Make a backup first if you must.

## Troubleshooting

| Problem | Fix |
|---|---|
| `database is locked` | Another long write is running (for example a big import). Wait and try again. If no moolah process is running, check for a stuck process with `pgrep moolah`. |
| Browser shows "invalid token" | Open the URL that `moolah serve` printed, including `?token=`. |
| Rates fail to download | Check the network, and run `moolah rates fetch -v` to see the request and the error. The Bank of Israel API sometimes changes; enter missing rates with `moolah price add` until moolah is updated. |
| Hebrew text looks reversed in the terminal | A terminal limitation. Use `--format csv` or the browser UI. |
| An import gave wrong amounts or dates | `moolah import undo <id>`, then report the problem with a sample file (personal details removed). |

## Development

The code is written mostly with Claude Code. The rules for the code are in `CLAUDE.md`. In short:

```
cmd/moolah/   main
core/         domain logic: accounts, transactions, balance rule, valuation, reports. No I/O.
store/        SQLite storage. The only package with SQL.
prices/       exchange-rate downloaders
importers/    file readers, bank profiles, duplicate detection, rules
app/          use cases. The CLI and the web UI call only this layer.
cli/          command-line interface (cobra)
web/          browser UI (Go templates + htmx), embedded in the binary
```

- `make check` must pass before every commit.
- Core rules have unit tests. Report output and import profiles have golden-file tests: `make golden` updates them after an intended change. Look at the diff before you commit.

### Adding an import profile

A profile is a small TOML file. No code change and no rebuild are needed. Profiles are stored in the book, so backups and sync include them.

1. Start from a similar profile: `moolah profile show generic-csv > my-bank.toml`.
2. Change the name, header names, columns and date format to match your file. Example:
   ```toml
   name = "my-bank"

   [detect]
   headers = ["תאריך", "תיאור", "חובה", "זכות"]

   [table]
   stop = "סה\"כ"

   [columns]
   date = "תאריך"
   date_format = "02/01/2006"
   payee = "תיאור"
   debit = "חובה"
   credit = "זכות"
   ```
3. Save it in moolah: `moolah profile add my-bank.toml`. moolah checks it and shows any error.
4. Test it without importing anything: `moolah import test ~/Downloads/export.xls --profile my-bank --account leumi`. Check that the dates, amounts and signs are correct.
5. Fix things with `moolah profile edit my-bank` (opens your `$EDITOR` and checks the profile when you save).
6. Import as usual. moolah finds the profile automatically from then on, and remembers which account it belongs to.

Other commands: `moolah profile list`, `moolah profile export my-bank > my-bank.toml` (to share it), `moolah profile remove my-bank`.

[docs/profiles.md](docs/profiles.md) lists every key, with a card example (pending rows, foreign purchases, installments). If a file cannot be described with the existing keys, the engine needs a new generic option. That is a code change in `importers/`, with a test.

**To add a built-in profile** (shipped in the binary): put the TOML in `importers/profiles/` with an anonymized sample file and a test. Remove personal details (names, account numbers, ID numbers) but keep the structure, headers and number formats exactly. Never commit a real bank export.

## License

MIT. See [LICENSE](LICENSE).
