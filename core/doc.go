// Package core holds moolah's domain model: accounts, transactions, money,
// exchange rates and the rules between them. It has no I/O: no SQL, no files,
// no network and no clock. Other packages load data, call core, and store results.
package core
