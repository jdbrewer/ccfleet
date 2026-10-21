//go:build !darwin

package fleet

import (
	"os"
	"strconv"
	"syscall"
	"time"
)

// procStart returns when pid started, or false if it is not running. /proc/<pid> is
// created at process start on Linux; elsewhere only liveness is known.
func procStart(pid int) (time.Time, bool) {
	if st, err := os.Stat("/proc/" + strconv.Itoa(pid)); err == nil {
		return st.ModTime(), true
	}
	if syscall.Kill(pid, 0) != nil {
		return time.Time{}, false
	}
	return time.Time{}, true
}
