# Import profiles

A profile is a small TOML file that tells moolah how to read one bank's or card's export file. moolah ships a few built-in profiles; you add your own with `moolah profile add`. Your profiles are stored in your book, so backups and sync include them.

```sh
moolah profile list                      # built-in and your own
moolah profile show generic-csv > my-bank.toml
# edit my-bank.toml
moolah profile add my-bank.toml          # checks it and saves it
moolah import test export.xls --profile my-bank --account leumi   # shows the rows, saves nothing
moolah profile edit my-bank              # change it later in $EDITOR
```

## File formats moolah reads

The format comes from the file's content, not its name:

- **CSV**: UTF-8 (with or without BOM) and UTF-16 with a BOM are recognized. Other files are read with the profile's `encoding` (default `windows-1252`). The separator (`,` `;` tab `|`) is detected.
- **XLSX**: the first sheet.
- **HTML saved as .xls**: many banks do this; moolah reads the largest table.
- **Old binary .xls** is not supported. Open it in a spreadsheet program and save it as .xlsx or .csv.

Cells are cleaned: invisible direction marks (U+200E, U+200F) are removed and spaces are collapsed.

## Keys

Every key below is optional unless it says **required**. Unknown keys are errors, so a typo never goes unnoticed.

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

To add a profile to moolah itself, put the TOML file in `importers/profiles/`, and add an anonymized sample file and a test. Remove names, account numbers and ID numbers, but keep the structure, headers and number formats exactly. Never commit a real bank export.
