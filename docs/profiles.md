# Import profiles

A profile is a small TOML file that tells moolah how to read one bank's or card's export file. moolah ships no bank profiles: you create one for each bank or card you use, once. Your profiles are stored in your book, so backups and sync include them.

## Create a profile from your bank's export

Download an export (CSV, XLSX, or the HTML file that many banks save as .xls) and run:

```sh
moolah profile create ~/Downloads/export.xlsx
```

moolah reads the file and finds the column names, the dates, the amounts and the description. It asks only what it cannot tell from the file:

- **Encoding**: for a text file that is not UTF-8, it shows one line read in several encodings. Choose the line you can read.
- **Day and month order**: `03/10/2026` can be 3 October or 10 March. moolah asks only when no day in the file is above 12.
- **Money out**: with two amount columns, which one is money out (debit). With one column, what a positive number means. It shows a real row, for example `Line 7: "COFFEE" shows 3.50. Did money leave the account?` Card exports usually show a purchase as a positive number.
- **Confirmations**: the payee column, the bank's reference number (used to recognize rows imported before), a card's charge date, and a total row at the end of the file where reading stops.
- **Card details**, only when the file has such columns:
  - **Currency**: a column of short values such as `USD`, `$` or `₪`. moolah asks whether it is the currency of the amount. ISO codes are read as they are; for a symbol such as `₪`, moolah asks for its code (`ILS`), because one symbol can mean more than one currency.
  - **Foreign purchases**: another amount column with a currency column, for the amount before conversion.
  - **Installments**: text such as `payment 3 of 12` or `תשלום 3 מתוך 12`, in any language. moolah shows a real row and builds the pattern from the file's own words.
  - **Pending rows**: a column with only a few repeated values, such as a status. Choose the value that marks rows that are not final yet, and moolah skips those rows.
  - **Note**: another text column with details to keep as the transaction's note.
- **Name**: for example the bank's name.

Then it shows a summary and the first rows as they would be imported:

```
   1  date         "Booking date", dd/mm/yyyy
   2  amount       debit "Debit" (money out), credit "Credit" (money in)
   3  currency     the account's currency
   4  foreign      none
   5  payee        "Text"
   6  note         none
   7  reference    "Reference"
   8  charge date  none
   9  installments none
  10  skip rows    none
  11  read until   the row with "Total"
  12  name         mybank
      Not used: "Balance"

The file as it would be imported (84 rows):
  line 4    2026-10-03  SUPERMARKET                            -123.4
  ...

[s]ave, [1-12] change a line, [e]dit the profile text, [q]uit [s]:
```

- Type a line number to answer that question again; the preview runs again.
- `e` opens the profile text in your `$EDITOR`, for anything the questions do not cover, such as more than one skip rule (see [Keys](#keys) below). It is checked when you close the editor.
- `s` saves the profile in the book. If a profile with that name exists, moolah shows the lines that change and asks before replacing it.
- `q` saves nothing.

Then import:

```sh
moolah import test ~/Downloads/export.xlsx --account checking   # shows the rows, saves nothing
moolah import ~/Downloads/export.xlsx --account checking
```

## Change a profile

- A small change: `moolah profile edit mybank` opens the saved profile in `$EDITOR`. If the result has an error, moolah shows it and offers to edit again.
- The bank changed its export: run `moolah profile create` on a new file, with the same name. moolah shows what changes before it replaces the old profile.
- Every saved version is kept in the book's history, so a bad change can be undone.

Other commands:

```sh
moolah profile list                     # your profiles, and the built-in generic-csv
moolah profile show mybank              # print it
moolah profile export mybank > mybank.toml   # a file to share
moolah profile add mybank.toml          # check a TOML file and save it in the book
moolah profile remove mybank
```

## Common problems

| What you see | What to change |
|---|---|
| `no import profile matches this file` | The column names changed, or there is no profile yet. Run `moolah profile create <file>`. |
| `the header row was not found` | The error shows the first rows of the file. Fix `[detect] headers` to match them. |
| Money in and money out are swapped | `negate = true` (or remove it). With two columns, swap `debit` and `credit`. |
| Text looks broken (`×ª×`, `Ã©`) | Set `encoding`, for example `windows-1255` for Hebrew. |
| `does not match date_format` | Fix `date_format` (see `[columns]` below). |
| A total or summary row is imported | `[table] stop = "Total"` (text from that row). |
| Pending card rows are imported | `[table] skip = [{ column = "Status", contains = "pending" }]` |
| `1.234,56` is read as 1.23456 | `decimal_comma = true` |

## File formats moolah reads

The format comes from the file's content, not its name:

- **CSV**: UTF-8 (with or without BOM) and UTF-16 with a BOM are recognized. Other files are read with the profile's `encoding` (default `windows-1252`). The separator (`,` `;` tab `|`) is detected.
- **XLSX**: the first sheet.
- **HTML saved as .xls**: many banks do this; moolah reads the largest table.
- **Old binary .xls** is not supported. Open it in a spreadsheet program and save it as .xlsx or .csv.

Cells are cleaned: invisible direction marks (U+200E, U+200F) are removed and spaces are collapsed.

## Keys

You need this section only to edit a profile by hand. Every key below is optional unless it says **required**. Unknown keys are errors, so a typo never goes unnoticed.

### Top level

| Key | Meaning |
|---|---|
| `name` | **required.** Lower-case letters, digits, `-` and `_`. |
| `description` | Shown in `moolah profile list`. |
| `currency` | Currency of the amounts when the file has no currency column. Default: the account's currency. |
| `encoding` | For text files that are **not** UTF-8: the file's character encoding, for example `windows-1252` (Western Europe), `windows-1255` (Hebrew), `windows-1251` (Cyrillic), `shift_jis`. Default `windows-1252`. UTF-8 files ignore it. |

### `[detect]`: recognizing the file

| Key | Meaning |
|---|---|
| `headers` | **required.** Header names that must all appear in one row. That row is the header row. Matching ignores case and extra spaces. |
| `file` | A file name pattern, for example `"*.xlsx"`. |

### `[table]`: where the data is

| Key | Meaning |
|---|---|
| `header_row` | Row number of the header row (1 = first row). Default `0`: the first row that contains all `detect.headers`. Title rows above it are ignored. |
| `stop` | Stop reading at the first row with a cell that contains this text, for example `"סה\"כ"` (a total row). |
| `skip` | A list of rules; a row is skipped when any rule matches. Each rule has `column` and either `contains = "text"` or `empty = true`. Use it for pending transactions and sub-totals. |

Empty rows are always skipped.

### `[columns]`: which column is which

Values are header names, exactly as in the file (case and extra spaces do not matter).

| Key | Meaning |
|---|---|
| `date` | **required.** Transaction (purchase) date. |
| `date_format` | A Go date layout: `2006` = year, `01` = month, `02` = day. For example `"02/01/2006"` for 31/12/2026, `"01/02/2006"` for 12/31/2026, `"02.01.06"` for 31.12.26. Without it, moolah reads only `YYYY-MM-DD` (and `YYYY/MM/DD`), because `03/10/2026` means different days in different countries. Excel date numbers always work. |
| `charge_date`, `charge_date_format` | The date a card charged the purchase. Used by `report income --by charge-date`. |
| `payee` | **required.** The description of the transaction. Rules match it. |
| `memo` | Extra text. Saved as the note; rules also match it. |
| `external_id` | The bank's reference number. When present, duplicate detection uses it. |
| `amount` | One signed amount column: negative = money left the account. **Either `amount`, or `debit` and/or `credit`, is required.** |
| `negate` | `true` flips the sign of `amount`. Card exports usually show a charge as a positive number; set `negate = true`. |
| `debit`, `credit` | Two columns instead of `amount`: `debit` (חובה) is money out, `credit` (זכות) is money in. Empty cells count as zero. |
| `decimal_comma` | `true` for numbers like `1.234,56`. |
| `currency` | A column with the amount's currency. Values go through `[currency_map]`. |
| `orig_amount`, `orig_currency` | For foreign purchases on a card: the original amount and currency. Saved with the transaction; the charged amount is what moolah records. |
| `installment`, `installment_pattern` | A column and a regular expression with two groups: payment number and number of payments, for example `'תשלום (\d+) מתוך (\d+)'`. Saved as "payment 3 of 12". |

Only `date`, `payee` and the amount columns must exist in the file. Other columns may be missing; they are then ignored.

Numbers may contain any currency symbol or code (`€`, `$`, `¥`, `CHF`, …), thousands separators (including the Swiss `1'234.50`), a trailing minus (`50.00-`) or parentheses (`(50.00)`).

### `[currency_map]`

Maps values in currency columns to ISO codes:

```toml
[currency_map]
"₪" = "ILS"
"$" = "USD"
"€" = "EUR"
```

## Example: a Hebrew card export (adjust to your file)

This example shows the shape of a typical Israeli card export: Hebrew headers, pending rows, foreign purchases and installments. Real column names differ between companies and change over time, so check them against your own file.

```toml
name = "my-card"
description = "My credit card (xlsx)"
currency = "ILS"

[detect]
headers = ["תאריך עסקה", "שם בית העסק", "סכום חיוב"]

[table]
skip = [{ column = "סטטוס", contains = "בקליטה" }]   # pending, not final yet
stop = "סה\"כ"

[columns]
date = "תאריך עסקה"
date_format = "02-01-2006"
charge_date = "תאריך חיוב"
payee = "שם בית העסק"
amount = "סכום חיוב"
negate = true
orig_amount = "סכום עסקה"
orig_currency = "מטבע עסקה"
installment = "הערות"
installment_pattern = 'תשלום (\d+) מתוך (\d+)'

[currency_map]
"₪" = "ILS"
"$" = "USD"
```

## Example: a Hebrew bank account with debit and credit columns

```toml
name = "my-bank"
encoding = "windows-1255"   # Hebrew CSV exports are often not UTF-8

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
external_id = "אסמכתא"
```

## Built-in profiles

moolah ships only `generic-csv` (columns Date, Description, Amount, with YYYY-MM-DD dates). It is a template, and it reads plain CSV files made by hand or by other programs. Bank profiles do not ship: each user creates their own.
