// Shared helpers for the app tests.
package app

import "github.com/shopspring/decimal"

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }
