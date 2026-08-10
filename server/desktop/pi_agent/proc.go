//go:build desktop

package pi_agent

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// procSpec describes the pi subprocess RunTurn wants started. Env entries are
// additions on top of the inherited environment — the pi binary needs PATH
// and HOME like any tool, plus our isolation overrides.
type procSpec struct {
	Bin  string
	Args []string
	Env  map[string]string
	Dir  string
}

// procHandle is a started subprocess, or a test fake. Wait is safe to call
// more than once (later calls return the first result); Kill is idempotent.
type procHandle interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Wait() error
	Kill()
}

// procStarter is the test seam mirroring local_agent's queryStarter: tests
// swap it for a fake that feeds scripted JSONL frames, so no pi binary is
// needed. stderrLine receives each stderr line as it arrives (logging plus
// the error-detail tail). Production is startPiProcess.
type procStarter func(ctx context.Context, spec procSpec, stderrLine func(string)) (procHandle, error)

// startPiProcess is the production starter: an exec.Cmd bound to ctx, so
// user cancellation kills the subprocess the same way the SDK tears down the
// claude CLI.
func startPiProcess(ctx context.Context, spec procSpec, stderrLine func(string)) (procHandle, error) {
	cmd := exec.CommandContext(ctx, spec.Bin, spec.Args...)
	cmd.Dir = spec.Dir
	env := os.Environ()
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			stderrLine(sc.Text())
		}
	}()
	return &execProc{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

type execProc struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	waitOnce sync.Once
	waitErr  error
}

func (p *execProc) Stdin() io.WriteCloser { return p.stdin }
func (p *execProc) Stdout() io.Reader     { return p.stdout }

func (p *execProc) Wait() error {
	p.waitOnce.Do(func() { p.waitErr = p.cmd.Wait() })
	return p.waitErr
}

func (p *execProc) Kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// waitWithTimeout reaps the process, killing it if a graceful exit (stdin
// EOF) takes longer than d. Used on the settled path only — a turn that
// already produced its result must not hang on subprocess shutdown.
func waitWithTimeout(p procHandle, d time.Duration) {
	done := make(chan struct{})
	go func() {
		_ = p.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		p.Kill()
		<-done
	}
}
