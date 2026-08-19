package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CommandStep is the primary ExecutionStep implementation.
// It runs a command WITHOUT shell — exec.Command(args...) directly.
// This is a security invariant: never use "sh -c".
type CommandStep struct {
	Name       string
	ID         string // stable step id; falls back to "step-<Index>" when empty
	Index      int    // position within the plan
	Executable string
	Args       []string
	Env        map[string]string
	WorkingDir string
	Timeout    time.Duration
}

func (s *CommandStep) Describe() string {
	parts := []string{s.Executable}
	parts = append(parts, s.Args...)
	return s.Name + ": " + strings.Join(parts, " ")
}

func (s *CommandStep) Execute(ctx Context) StepResult {
	start := time.Now()

	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Remote execution: if the Context carries a target host, run over SSH
	// instead of locally. This is the control plane → managed host path.
	if target := ctx.Target(); !target.IsZero() {
		return runRemote(ctx, target, s, cmdCtx, timeout)
	}

	// SECURITY: exec.Command with args array — never shell.
	cmd := exec.CommandContext(cmdCtx, s.Executable, s.Args...)

	if s.WorkingDir != "" {
		cmd.Dir = s.WorkingDir
	}
	if len(s.Env) > 0 {
		cmd.Env = buildEnv(s.Env)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)

	stepID := s.ID
	if stepID == "" {
		stepID = fmt.Sprintf("step-%d", s.Index)
	}
	result := StepResult{
		StepName: s.Name,
		StepID:   stepID,
		Index:    s.Index,
		Output:   stdout.String(),
		Duration: duration,
	}

	if err != nil {
		result.Success = false
		result.Error = err
		if stderr.Len() > 0 {
			result.Output = stderr.String()
		}
	} else {
		result.Success = true
	}

	ctx.Logger().Debug("step_executed",
		"step", s.Name,
		"success", result.Success,
		"duration", duration,
	)

	return result
}

func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// runRemote executes a CommandStep on a remote host via SSH.
func runRemote(ctx Context, target TargetHost, s *CommandStep, cmdCtx context.Context, timeout time.Duration) StepResult {
	start := time.Now()
	so, se, err := RunOverSSH(cmdCtx, target, s.Executable, s.Args, s.Env, timeout)
	duration := time.Since(start)

	stepID := s.ID
	if stepID == "" {
		stepID = fmt.Sprintf("step-%d", s.Index)
	}
	res := StepResult{
		StepName: s.Name,
		StepID:   stepID,
		Index:    s.Index,
		Output:   so,
		Duration: duration,
	}
	if err != nil {
		res.Success = false
		res.Error = err
		if se != "" {
			res.Output = se
		}
	} else {
		res.Success = true
	}

	ctx.Logger().Debug("step_executed_remote",
		"step", s.Name,
		"host", target.Address,
		"success", res.Success,
		"duration", duration,
	)
	return res
}
