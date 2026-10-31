//go:build race

package schema

// raceSlowdown scales time bounds in robustness tests: the race detector makes
// the quadratic-ish schema cleaner on huge inputs roughly ten times slower.
const raceSlowdown = 12
