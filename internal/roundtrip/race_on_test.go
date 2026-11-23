//go:build race

package roundtrip_test

// raceEnabled is true when the race detector runs. The matrix tests then run every second case and the chain
// matrix runs its sample, so the race run of the release workflow stays inside one test timeout.
const raceEnabled = true
