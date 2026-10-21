package fleet

import (
	"time"

	"golang.org/x/sys/unix"
)

// procStart returns when pid started, or false if it is not running.
func procStart(pid int) (time.Time, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return time.Time{}, false
	}
	return time.Unix(kp.Proc.P_starttime.Sec, int64(kp.Proc.P_starttime.Usec)*1000), true
}
