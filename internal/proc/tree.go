package proc

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// A Tree is a started command and the processes it starts in turn: an
// agent's CLI with the MCP servers, shells and helpers it runs. Killing the
// CLI alone leaves those running, so they are ended with it.
type Tree struct {
	cmd *exec.Cmd

	mu      sync.Mutex
	done    bool          // waited for: its pid may be another process's now
	waited  chan struct{} // closed once Wait has
	stopped chan struct{} // Stop's, closed once it has ended the tree
	group
}

// A failure is why a tree could not be ended: the signal was refused (the
// container's AppArmor, #6599), so the processes are still there and the
// memory they hold is not free.
type failure struct {
	mu  sync.Mutex
	err error
}

// fail remembers why a tree could not be ended, the first reason given.
func (f *failure) fail(err error) {
	if err == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err == nil {
		f.err = err
	}
}

// why is why a tree could not be ended, if it could not be.
func (f *failure) why() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// StopErr is why the tree could not be ended, if it could not be: the
// signal was refused, and its processes are still there (#6599).
func (t *Tree) StopErr() error { return t.group.why() }

// StartTree starts cmd in a group of its own: a session on Unix, a job on
// Windows. It has no terminal then, and a Ctrl+C there doesn't reach it.
func StartTree(cmd *exec.Cmd) (*Tree, error) {
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t := &Tree{cmd: cmd, waited: make(chan struct{})}
	t.join(cmd)
	return t, nil
}

// Wait waits for the command, then ends what it left running.
func (t *Tree) Wait() error {
	err := t.cmd.Wait()
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done {
		t.done = true
		close(t.waited)
	}
	t.sweep(t.cmd)
	return err
}

// Kill ends the command and every process of its tree.
func (t *Tree) Kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	_ = t.kill(t.cmd)
	t.killRoot()
}

// killRoot ends the command itself, and remembers why it could not be
// ended: the signal was refused and it is still there (#6599).
func (t *Tree) killRoot() {
	err := t.cmd.Process.Kill()
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return
	}
	t.group.fail(err)
	log.Printf("proc: the command %d could not be ended: %v", t.cmd.Process.Pid, err)
}

// Stop asks the command to end, so it can clean up first, and ends its tree
// once it has (Wait sweeps what it left) or grace has passed (Kill). Claude
// Code holding its OAuth refresh lock removes it as it exits on SIGTERM, and
// a SIGKILL left ~/.claude/.oauth_refresh.lock behind for every later
// request to wait on (#2195). Windows has no such signal: the tree is killed
// at once there. It returns at once; the channel closes once the tree is
// ended. A tree no one Waits for is killed after grace.
func (t *Tree) Stop(grace time.Duration) <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped != nil {
		return t.stopped
	}
	t.stopped = make(chan struct{})
	if t.done || t.term(t.cmd) != nil {
		if !t.done {
			_ = t.kill(t.cmd)
			t.killRoot()
		}
		close(t.stopped)
		return t.stopped
	}
	go func() {
		defer close(t.stopped)
		select {
		case <-t.waited:
		case <-time.After(grace):
			t.Kill()
		}
	}()
	return t.stopped
}
