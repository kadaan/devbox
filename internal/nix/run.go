// Copyright 2024 Jetify Inc. and contributors. All rights reserved.
// Use of this source code is governed by the license in the LICENSE file.

package nix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"go.jetify.com/devbox/internal/boxcli/usererr"
	"go.jetify.com/devbox/internal/cmdutil"
)

func RunScript(ctx context.Context, projectDir string, cmdWithArgs []string, env map[string]string) error {
	if len(cmdWithArgs) == 0 {
		return errors.New("attempted to run an empty command or script")
	}

	envPairs := []string{}
	for k, v := range env {
		envPairs = append(envPairs, fmt.Sprintf("%s=%s", k, v))
	}

	// Wrap in quotations since the command's path may contain spaces.
	cmdWithArgs[0] = "\"" + cmdWithArgs[0] + "\""
	cmdWithArgsStr := strings.Join(cmdWithArgs, " ")

	// Try to find sh in the PATH, if not, default to a well-known absolute path.
	shPath := cmdutil.GetPathOrDefault("sh", "/bin/sh")
	cmd := exec.CommandContext(ctx, shPath, "-c", cmdWithArgsStr)
	cmd.Env = envPairs
	cmd.Dir = projectDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Cancel = func() error {
		return syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
	}

	slog.Debug("executing script", "cmd", cmd.Args)
	// Report error as exec error when executing scripts.
	return usererr.NewExecError(cmd.Run())
}
