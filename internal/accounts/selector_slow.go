// Part of the accounts package — see firsttoken.go. Selector helpers that keep
// new work off an account whose first-token time is currently bad.
package accounts

// shunned reports whether acc should be skipped for this pick: it is slow AND
// some other usable account is not. When every usable account is slow (or acc
// is the only one) nothing is shunned, so a pool-wide slowdown never leaves a
// request without an account.
func shunned(acc *Record, accountList []*Record, isExcluded func(*Record) bool, model string) bool {
	if !IsSlowAccount(acc.Email) {
		return false
	}
	for _, other := range accountList {
		if other.Email == acc.Email || isExcluded(other) || !availableFor(other, model) {
			continue
		}
		if !IsSlowAccount(other.Email) {
			return true
		}
	}
	return false
}
