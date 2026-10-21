// Command fakeclaude stands in for a Claude Code process in the demo fleet: it only
// needs to stay alive under a name ccfleet's liveness check accepts.
package main

import "time"

func main() { time.Sleep(24 * time.Hour) }
