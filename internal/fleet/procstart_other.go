//go:build !darwin

package fleet

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// procParent returns pid's parent pid, or 0.
func procParent(pid int) int {
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		// pid (comm) state ppid ...; comm may contain spaces, so split after the last ')'
		if i := strings.LastIndexByte(string(b), ')'); i >= 0 {
			if f := strings.Fields(string(b)[i+1:]); len(f) > 1 {
				n, _ := strconv.Atoi(f[1])
				return n
			}
		}
	}
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}
