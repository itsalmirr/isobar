// Package failing exercises the analyzer's three banned shapes (direct
// reads of Now, Since, Until from package time). Each violation lives on
// its own line below so the analysistest expectations match exactly.
package failing

import (
	"time"
)

func directNow() time.Time {
	return time.Now() // want "time.Now is restricted"
}

func directSince(t time.Time) time.Duration {
	return time.Since(t) // want "time.Since is restricted"
}

func directUntil(t time.Time) time.Duration {
	return time.Until(t) // want "time.Until is restricted"
}
