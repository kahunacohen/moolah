# Exchange-rate sources

moolah downloads exchange rates only from **rate sources**: small TOML files that describe a public rates API. No source is built into the code. The sources that ship with moolah are TOML files in `prices/sources/`, and you can add your own the same way. Your sources are stored in your book, so backups and sync include them.

```sh
moolah source list                       # all sources; * = used
moolah source show ecb > my-source.toml  # a starting point
moolah source add my-source.toml         # checks it and saves it
moolah source test my-source USD CAD     # downloads a week of rates, saves nothing
moolah source edit my-source             # change it in $EDITOR
moolah source remove my-source
```

## Which source is used

For every currency your book uses, moolah needs its rate **into the report currency** (your base currency, or the one you give with `--in`).

1. moolah tries the sources in order, and uses the first one that publishes the pair.
2. If no source publishes the pair, it looks for a currency that both sides can be converted to, and downloads two rates. Reports then combine them into a cross rate.
3. If that also fails, the error names the pair. Add rates by hand with `moolah price add` or `moolah price import`, or add a source.

**Order:** set it with `moolah config set rate_sources name1,name2`. Only the listed sources are used. Without the setting, moolah uses all sources: first those that publish against your base currency (usually your country's central bank), then those that publish any pair, then the rest. On the same day, a rate from an earlier source wins over a later one. Any downloaded rate wins over a manual rate, and a manual rate wins over a rate taken from your own transactions.

## Built-in sources

| Name | Publishes | Notes |
|---|---|---|
| `ecb` | any pair among about 30 currencies | European Central Bank reference rates, through the free Frankfurter API |
| `boi` | 14 currencies → ILS | Bank of Israel representative rates |

To replace a built-in source, add a source with the same name.

## Keys

Unknown keys are errors.

| Key | Meaning |
|---|---|
| `name` | **required.** Lower-case letters, digits, `-` and `_`. `manual` and `tx` are reserved. |
| `description` | Shown in `moolah source list`. |
| `currencies` | **required.** ISO codes the source publishes. |
| `quote` | When set, the source publishes only rates **into** this currency (`X → quote` for each X in `currencies`). Without it, it publishes any pair among `currencies`. |
| `url` | **required.** The address, with placeholders `{base}`, `{quote}`, `{from}`, `{to}` (dates as `YYYY-MM-DD`). One request returns the daily rates of one pair for a date range. |
| `format` | **required.** `json` or `csv`. |
| `rate` | **required.** The CSV column, or the JSON path inside a row, of the rate (1 base = rate quote). May contain placeholders, for example `"{quote}"` or `"FX{base}CAD.v"`. |
| `date` | The CSV column or JSON path of the date in a row. Required unless `rows_keyed_by_date = true`. |
| `date_format` | Go date layout of the dates. Default `2006-01-02` (`YYYY-MM-DD`). |
| `rows` | JSON only, **required**: the path to the rows, a list of objects, or an object whose keys are dates. |
| `rows_keyed_by_date` | JSON only: `true` when `rows` is an object like `{"2026-10-01": {...}, ...}`. |
| `unit_power` | The CSV column or JSON path of a number *n*: the rate is for 10ⁿ units. For example, a rate per 100 JPY has *n* = 2. |
| `max_days` | The longest date range per request, for APIs with a limit. Longer ranges are split. |

A JSON path is keys separated by dots: `data.rates`, `FXUSDCAD.v`.

If the answer is an HTML page (an error page) or HTTP 404, moolah treats the pair as "not published" and tries the next source.

## Examples

**Any pair, JSON keyed by date** (the built-in `ecb`):

```toml
name = "ecb"
currencies = ["EUR", "USD", "GBP", "JPY"]   # shortened
url = "https://api.frankfurter.dev/v1/{from}..{to}?from={base}&to={quote}"
format = "json"
rows = "rates"
rows_keyed_by_date = true
rate = "{quote}"
```

**Into one currency, JSON list** (Bank of Canada; not built in):

```toml
name = "boc"
description = "Bank of Canada daily rates (against CAD)"
quote = "CAD"
currencies = ["USD", "EUR"]
url = "https://www.bankofcanada.ca/valet/observations/FX{base}CAD/json?start_date={from}&end_date={to}"
format = "json"
rows = "observations"
date = "d"
rate = "FX{base}CAD.v"
```

**Into one currency, CSV with a unit column** (the built-in `boi`):

```toml
name = "boi"
quote = "ILS"
currencies = ["USD", "EUR", "JPY"]   # shortened
url = "https://edge.boi.gov.il/FusionEdgeServer/sdmx/v2/data/dataflow/BOI.STATISTICS/EXR/1.0/RER_{base}_ILS?startperiod={from}&endperiod={to}&format=csv"
format = "csv"
date = "TIME_PERIOD"
rate = "OBS_VALUE"
unit_power = "UNIT_MULT"
```

## Limits

A source must answer one plain HTTP GET per pair and date range, with CSV or JSON. APIs that need a login or an API key, paging, or XML are not supported yet. Supporting them is a generic engine change in `prices/`, with a test.
