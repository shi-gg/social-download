//go:build !unix

package app

import "os/exec"

func configureCommand(cmd *exec.Cmd) {}

func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
