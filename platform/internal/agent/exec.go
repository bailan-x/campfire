package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// runner executes commands on the host (the Agent runs as root, but game files
// belong to the game user, so most file work is delegated through runAsGameUser).
type runner struct {
	GameUser string // e.g. "dst"
	Home     string // e.g. "/home/dst"
}

func (r *runner) logf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "[agent] "+format+"\n", args...)
}

// run executes a command and returns combined output.
func (r *runner) run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 4000 {
			msg = msg[len(msg)-4000:]
		}
		return msg, fmt.Errorf("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

// runAsGameUser runs a command as the game account (unset HOME, no login shell).
func (r *runner) runAsGameUser(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	full := append([]string{"-u", r.GameUser, "--", name}, args...)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "runuser", full...)
	cmd.Env = append(os.Environ(), "HOME="+r.Home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 4000 {
			msg = msg[len(msg)-4000:]
		}
		return msg, fmt.Errorf("%s: %v", strings.Join(full, " "), err)
	}
	return string(out), nil
}

// chown fixes ownership of a tree to the game account.
func (r *runner) chown(ctx context.Context, path string) error {
	_, err := r.run(ctx, 2*time.Minute, "chown", "-R", r.GameUser+":"+r.GameUser, path)
	return err
}

// serviceState returns (active, restarts).
func (r *runner) serviceState(ctx context.Context, unit string) (bool, int) {
	out, err := r.run(ctx, 15*time.Second, "systemctl", "is-active", unit)
	active := err == nil && strings.TrimSpace(out) == "active"
	out2, _ := r.run(ctx, 15*time.Second, "systemctl", "show", "-p", "NRestarts", "--value", unit)
	restarts, _ := strconv.Atoi(strings.TrimSpace(out2))
	return active, restarts
}

func (r *runner) restartService(ctx context.Context, unit string) error {
	_, err := r.run(ctx, 3*time.Minute, "systemctl", "restart", unit)
	return err
}

func (r *runner) startService(ctx context.Context, unit string) error {
	_, err := r.run(ctx, 2*time.Minute, "systemctl", "start", unit)
	return err
}

func (r *runner) stopService(ctx context.Context, unit string) error {
	_, err := r.run(ctx, 2*time.Minute, "systemctl", "stop", unit)
	return err
}

// waitStarted tails the unit journal until the shard reports Online Server
// Started, or the timeout expires.
func (r *runner) waitStarted(ctx context.Context, unit string, since time.Time, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("%s 未在 %s 内启动完成（等 Online Server Started 超时）", unit, timeout)
		}
		out, _ := r.run(ctx, 30*time.Second, "journalctl", "-u", unit,
			"--since", since.Format("2006-01-02 15:04:05"), "--no-pager", "-o", "cat")
		if strings.Contains(out, "Online Server Started on port:") &&
			strings.Contains(lastLines(out, 40), "Sim paused") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// newLineScanner builds a line scanner with a generous buffer (workshop names
// and steamcmd progress lines can be long).
func newLineScanner(r io.Reader, buf *[]byte) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(*buf, 4*1024*1024)
	return sc
}

// readLines reads a small text file.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out, sc.Err()
}

// writeFileAtomic writes path via a temp file + rename, preserving mode.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".campfire.tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// fileSize returns the size of path, or 0 when it does not exist yet.
func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// readNewBytes returns everything appended to path after offset (offset 0 when
// the file was truncated or rotated), plus the new offset.
func readNewBytes(path string, offset int64) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", offset, nil
		}
		return "", offset, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", offset, err
	}
	if st.Size() < offset {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", offset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", offset, err
	}
	return string(data), offset + int64(len(data)), nil
}

// waitLogLineNew waits until path has been (re)written after the given moment and
// contains a line matching re. DST truncates server_log.txt on every start, so a
// byte offset captured before the process starts can skip the beginning of the new
// run (the old file may be smaller than the new one). Checking mtime first makes
// sure we only read lines written by the run we just started.
func (r *runner) waitLogLineNew(ctx context.Context, path string, re *regexp.Regexp, after time.Time, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if st, err := os.Stat(path); err == nil && st.ModTime().After(after) {
			data, _, err := readNewBytes(path, 0)
			if err == nil {
				for _, line := range strings.Split(data, "\n") {
					if re.MatchString(line) {
						return strings.TrimSpace(line), nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s 在 %s 内没有出现匹配 %q 的行", path, timeout, re.String())
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(600 * time.Millisecond):
		}
	}
}

// waitLogLine waits until a line matching re appears in the part of path that
// was appended after offset. Shard logs use the in-game clock, so file offsets
// (not wall-clock filters) are the reliable way to read the current run.
func (r *runner) waitLogLine(ctx context.Context, path string, offset int64, re *regexp.Regexp, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		chunk, next, err := readNewBytes(path, offset)
		if err == nil {
			offset = next
			for _, line := range strings.Split(chunk, "\n") {
				if re.MatchString(line) {
					return strings.TrimSpace(line), nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s 在 %s 内没有出现匹配 %q 的行", path, timeout, re.String())
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(600 * time.Millisecond):
		}
	}
}