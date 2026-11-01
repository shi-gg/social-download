//go:build unix

package app

import (
	"os/exec"
	"syscall"
)

// configureCommand puts the child in its own process group so that yt-dlp's
// ffmpeg children are killed along with yt-dlp when a job is canceled.
func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
