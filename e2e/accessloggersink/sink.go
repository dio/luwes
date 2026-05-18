// Package accessloggersink is a test-only in-process sink for access log entries.
// The e2e access logger filter calls Push for each log event; e2e tests call
// Drain to collect and assert on the entries.
package accessloggersink

import "time"

// Entry is one access log event delivered from the module under test.
type Entry struct {
	LogType       int32
	DurationMs    float64
	BytesSent     uint64
	BytesReceived uint64
	ResponseCode  uint32
	ResponseFlags uint64
	CodeDetails   string
	RequestID     string
}

var ch = make(chan Entry, 64)

// Push delivers an entry to the sink. Called by the access logger module.
// Must not block; the channel is large enough for all reasonable test volumes.
func Push(e Entry) { ch <- e }

// Drain collects all entries received within timeout.
// Blocks until at least one entry arrives or timeout expires.
// Returns whatever arrived within the window.
func Drain(timeout time.Duration) []Entry {
	var entries []Entry
	deadline := time.After(timeout)
	for {
		select {
		case e := <-ch:
			entries = append(entries, e)
			// Drain any additional entries already queued, without waiting.
		drain:
			for {
				select {
				case e2 := <-ch:
					entries = append(entries, e2)
				default:
					break drain
				}
			}
			return entries
		case <-deadline:
			return entries
		}
	}
}

// Reset discards all pending entries. Call at the start of each test.
func Reset() {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
