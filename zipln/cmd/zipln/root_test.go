package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eskelinenantti/zipln/internal/project"
	"github.com/eskelinenantti/zipln/internal/shell"
	"github.com/eskelinenantti/zipln/internal/shell/path"
	"github.com/eskelinenantti/zipln/internal/test/mock"
	"github.com/eskelinenantti/zipln/internal/test/spy"
	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
)

const editor string = "editor"

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestExecuteErrors(t *testing.T) {
	for _, tt := range []struct {
		name, mux, herdrEnv string
		err                 error
		wait                bool
		want                string
	}{
		{"missing fd in popup", "herdr", "", shell.NotInstalledError{Cmd: "fd"}, true, "brew install fd\n"},
		{"missing fzf in popup", "herdr", "", shell.NotInstalledError{Cmd: "fzf"}, true, "brew install fzf\n"},
		{"missing multiple tools", "herdr", "", shell.MissingToolsError{Tools: []string{"fd", "fzf"}}, true, "brew install fd fzf\n"},
		{"auto Herdr", "auto", "1", errors.New("workspace failed"), true, "workspace failed"},
		{"editor configuration", "herdr", "1", ErrEditorEnvNotSet, true, "export EDITOR=vim"},
		{"tmux override", "tmux", "1", errors.New("failed"), true, "failed"},
		{"auto outside Herdr", "auto", "", errors.New("failed"), true, "failed"},
		{"success or cancellation", "herdr", "1", nil, false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if strings.HasPrefix(tt.want, "brew install") && !(path.Path{}).Contains("brew") {
				tt.want = "Install the missing tools with your package manager"
			}
			t.Setenv("HERDR_ENV", tt.herdrEnv)
			cmd := &cobra.Command{Use: "zipln", RunE: func(*cobra.Command, []string) error { return tt.err }}
			cmd.Flags().String("mux", "auto", "")
			cmd.SetArgs([]string{"--mux", tt.mux})
			var output bytes.Buffer
			cmd.SetErr(&output)
			read := false
			cmd.SetIn(readerFunc(func(p []byte) (int, error) {
				read = true
				if !strings.Contains(output.String(), tt.want) || !strings.Contains(output.String(), "Press Enter") {
					t.Fatalf("input read before displaying guidance: %q", output.String())
				}
				return copy(p, "\n"), nil
			}))
			code := execute(cmd)
			if (code != 0) != (tt.err != nil) || read != tt.wait {
				t.Fatalf("exit code = %d, read input = %v; want error = %v, wait = %v", code, read, tt.err != nil, tt.wait)
			}
			if !strings.Contains(output.String(), tt.want) {
				t.Fatalf("output = %q, want %q", output.String(), tt.want)
			}
		})
	}
}

func TestExecuteKeepsHerdrErrorOpenUntilAcknowledged(t *testing.T) {
	for _, acknowledgment := range []string{"\n", "text\n", ""} {
		t.Run(fmt.Sprintf("input %q", acknowledgment), func(t *testing.T) {
			cmd := &cobra.Command{Use: "zipln", RunE: func(*cobra.Command, []string) error {
				return shell.NotInstalledError{Cmd: "fzf"}
			}}
			cmd.Flags().String("mux", "herdr", "")
			cmd.SetArgs([]string{})
			cmd.SetErr(io.Discard)
			input, writer := io.Pipe()
			t.Cleanup(func() { input.Close(); writer.Close() })
			reading := make(chan struct{})
			var started bool
			cmd.SetIn(readerFunc(func(p []byte) (int, error) {
				if !started {
					close(reading)
					started = true
				}
				return input.Read(p)
			}))
			done := make(chan int, 1)
			go func() { done <- execute(cmd) }()
			select {
			case <-reading:
			case <-time.After(5 * time.Second):
				t.Fatal("did not wait for acknowledgment")
			}
			select {
			case <-done:
				t.Fatal("exited before acknowledgment")
			default:
			}
			if acknowledgment != "" {
				_, err := io.WriteString(writer, acknowledgment)
				requireNoError(t, err)
			}
			writer.Close()
			select {
			case code := <-done:
				if code != 1 {
					t.Fatalf("exit code = %d, want 1", code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("did not exit after acknowledgment or EOF")
			}
		})
	}
}

func TestInstallHelpHomebrew(t *testing.T) {
	for _, hasBrew := range []bool{true, false} {
		t.Run(fmt.Sprintf("brew installed %t", hasBrew), func(t *testing.T) {
			shellPath := mock.Path{}
			if !hasBrew {
				shellPath.Missing = []string{"brew"}
			}
			help := installHelp([]string{"fd", "fzf"}, shellPath)
			if hasBrew {
				if !strings.Contains(help, "brew install fd fzf\n") {
					t.Fatalf("missing Homebrew command: %s", help)
				}
			} else {
				if strings.Contains(help, "brew") {
					t.Fatalf("Homebrew suggested without brew on PATH: %s", help)
				}
				if !strings.Contains(help, "your package manager") || !strings.Contains(help, "$PATH") {
					t.Fatalf("missing installation guidance: %s", help)
				}
			}
		})
	}
}

func TestRunMuxOverride(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("EDITOR", editor)
	dir := t.TempDir()
	runner := &spy.SpyRunner{}
	err := Run([]string{dir}, "tmux", runner, mock.Path{Missing: []string{"tmux"}})
	var missing shell.NotInstalledError
	if !errors.As(err, &missing) || missing.Cmd != "tmux" {
		t.Fatalf("explicit multiplexer was not selected: %v", err)
	}
	requireCalls(t, nil, runner.Calls)
}

func unsetenv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireCalls(t *testing.T, want, got [][]string) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func createFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSelectFolderFromPrompt(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_SESSION", "")
	tests := []struct {
		name     string
		attached bool
	}{
		{name: "attaches to session"},
		{name: "switches to session", attached: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EDITOR", editor)
			if tt.attached {
				t.Setenv("TMUX", "test")
			} else {
				unsetenv(t, "TMUX")
			}

			home := t.TempDir()
			t.Setenv("HOME", home)
			folder := "session"
			if err := os.Mkdir(filepath.Join(home, folder), 0755); err != nil {
				t.Fatal(err)
			}

			spyRunner := &spy.SpyRunner{
				Responses: []spy.Response{
					{}, {},
					{OnRun: mock.WriteToStdout(folder)},
				},
			}
			err := Run([]string{}, "auto", spyRunner, mock.Path{})
			requireNoError(t, err)

			session := project.Name(filepath.Join(home, folder))
			expectedCalls := [][]string{
				{"fd", "--max-depth", "2", "--follow", "--hidden", "--exclude", "{.git,node_modules,Library}", ".", "--base-directory", home},
				{"fd", "--min-depth", "3", "--follow", "--hidden", "--exclude", "{.git,node_modules,Library}", ".", "--base-directory", home},
				{"fzf", "--reverse", "--height", "70%", "--tmux", "70%", "--no-sort"},
				{"tmux", "has-session", "-t", session + ":"},
			}
			if tt.attached {
				expectedCalls = append(expectedCalls, []string{"tmux", "switch-client", "-t", session + ":"})
			} else {
				expectedCalls = append(expectedCalls, []string{"tmux", "attach", "-t", session + ":"})
			}

			requireCalls(t, expectedCalls, spyRunner.Calls)
		})
	}
}

func TestFolderSessionWorkflow(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_SESSION", "")
	tests := []struct {
		name          string
		attached      bool
		sessionExists bool
		nested        bool
	}{
		{name: "creates and attaches to session", nested: true},
		{name: "switches to existing session", attached: true, sessionExists: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EDITOR", editor)
			if tt.attached {
				t.Setenv("TMUX", "test")
			} else {
				unsetenv(t, "TMUX")
			}

			dir := t.TempDir()
			if tt.nested {
				dir = filepath.Join(dir, "path/to/dir/in/repository")
				if err := os.MkdirAll(dir, os.ModePerm); err != nil {
					t.Fatal(err)
				}
			}
			session := project.Name(dir)

			spyRunner := &spy.SpyRunner{}
			if !tt.sessionExists {
				spyRunner.Responses = []spy.Response{{OnRun: mock.SimulateError}}
			}

			err := Run([]string{dir}, "auto", spyRunner, mock.Path{})
			requireNoError(t, err)

			expectedCalls := [][]string{
				{"tmux", "has-session", "-t", session + ":"},
			}
			if !tt.sessionExists {
				expectedCalls = append(expectedCalls, []string{"tmux", "new-session", "-c", dir, "-d", "-s", session})
			}
			if tt.attached {
				expectedCalls = append(expectedCalls, []string{"tmux", "switch-client", "-t", session + ":"})
			} else {
				expectedCalls = append(expectedCalls, []string{"tmux", "attach", "-t", session + ":"})
			}

			requireCalls(t, expectedCalls, spyRunner.Calls)
		})
	}
}

func TestTmuxNotInstalled(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_SESSION", "")
	unsetenv(t, "TMUX")
	t.Setenv("EDITOR", editor)

	spyRunner := &spy.SpyRunner{}

	err := Run([]string{}, "auto", spyRunner, mock.Path{Missing: []string{"tmux"}})

	expectedError := shell.NotInstalledError{Cmd: "tmux"}
	var cmdNotInstalledError shell.NotInstalledError
	if !errors.As(err, &cmdNotInstalledError) {
		t.Fatalf("got=%v, want=%v", err, expectedError)
	}
	if cmdNotInstalledError != expectedError {
		t.Fatalf("got=%v, want=%v", cmdNotInstalledError, expectedError)
	}

	requireCalls(t, nil, spyRunner.Calls)
}

func TestEditorSessionWorkflow(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_SESSION", "")
	tests := []struct {
		name          string
		attached      bool
		sessionExists bool
	}{
		{name: "creates and attaches to session"},
		{name: "creates and switches to session", attached: true},
		{name: "opens new editor window and attaches to session", sessionExists: true},
		{name: "opens new editor window and switches to session", attached: true, sessionExists: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EDITOR", editor)
			if tt.attached {
				t.Setenv("TMUX", "test")
			} else {
				unsetenv(t, "TMUX")
			}

			dir := t.TempDir()
			file := createFile(t, dir, "file.txt")
			session := project.Name(dir)

			responses := []spy.Response{{OnRun: mock.SimulateError}}
			if !tt.sessionExists {
				responses = append(responses, spy.Response{OnRun: mock.SimulateError})
			}
			spyRunner := &spy.SpyRunner{Responses: responses}

			err := Run([]string{file}, "auto", spyRunner, mock.Path{})
			requireNoError(t, err)

			expectedCalls := [][]string{
				{"git", "-C", dir, "rev-parse", "--show-toplevel"},
				{"tmux", "has-session", "-t", session + ":"},
			}
			if tt.sessionExists {
				expectedCalls = append(expectedCalls,
					[]string{"tmux", "new-window", "-t", session + ":", "-c", dir, editor, file},
				)
			} else {
				expectedCalls = append(expectedCalls,
					[]string{"tmux", "new-session", "-c", dir, "-d", "-s", session, editor, file},
				)
			}
			if tt.attached {
				expectedCalls = append(expectedCalls, []string{"tmux", "switch-client", "-t", session + ":"})
			} else {
				expectedCalls = append(expectedCalls, []string{"tmux", "attach", "-t", session + ":"})
			}

			requireCalls(t, expectedCalls, spyRunner.Calls)
		})
	}
}

func TestRelativePathToFile(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_SESSION", "")
	unsetenv(t, "TMUX")
	t.Setenv("EDITOR", editor)

	dir := t.TempDir()
	fileName := "file.txt"
	createFile(t, dir, fileName)

	t.Chdir(dir)

	session := project.Name(dir)

	spyRunner := &spy.SpyRunner{
		Responses: []spy.Response{
			{OnRun: mock.SimulateError},
			{OnRun: mock.SimulateError},
		},
	}

	err := Run([]string{fileName}, "auto", spyRunner, mock.Path{})
	requireNoError(t, err)

	expectedCalls := [][]string{
		{"git", "-C", ".", "rev-parse", "--show-toplevel"},
		{"tmux", "has-session", "-t", session + ":"},
		{"tmux", "new-session", "-c", dir, "-d", "-s", session, editor, filepath.Join(dir, fileName)},
		{"tmux", "attach", "-t", session + ":"},
	}

	requireCalls(t, expectedCalls, spyRunner.Calls)
}

func TestFileDoesNotExist(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_SESSION", "")
	unsetenv(t, "TMUX")
	t.Setenv("EDITOR", editor)

	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")

	spyRunner := &spy.SpyRunner{}

	err := Run([]string{file}, "auto", spyRunner, mock.Path{})

	if !errors.Is(err, project.ErrInvalidPath) {
		t.Fatalf("got=%v, want=%v", err, project.ErrInvalidPath)
	}

	requireCalls(t, nil, spyRunner.Calls)
}

func TestFileInRepository(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_SESSION", "")
	unsetenv(t, "TMUX")
	t.Setenv("EDITOR", editor)

	repository := t.TempDir()
	file := createFile(t, repository, "file.txt")

	session := project.Name(repository)

	spyRunner := &spy.SpyRunner{
		Responses: []spy.Response{
			{OnRun: mock.WriteToStdout(repository)},
			{OnRun: mock.SimulateError},
		},
	}

	err := Run([]string{file}, "auto", spyRunner, mock.Path{})
	requireNoError(t, err)

	expectedCalls := [][]string{
		{"git", "-C", repository, "rev-parse", "--show-toplevel"},
		{"tmux", "has-session", "-t", session + ":"},
		{"tmux", "new-session", "-c", repository, "-d", "-s", session, editor, file},
		{"tmux", "attach", "-t", session + ":"},
	}

	requireCalls(t, expectedCalls, spyRunner.Calls)
}

func TestEditorNotSet(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_SESSION", "")
	unsetenv(t, "EDITOR")
	unsetenv(t, "TMUX")

	dir := t.TempDir()

	spyRunner := &spy.SpyRunner{}

	err := Run([]string{dir}, "auto", spyRunner, mock.Path{})

	if !errors.Is(err, ErrEditorEnvNotSet) {
		t.Fatalf("got=%v, want=%v", err, ErrEditorEnvNotSet)
	}
	requireCalls(t, nil, spyRunner.Calls)
}

func TestEditorNotInstalled(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_SESSION", "")
	t.Setenv("EDITOR", editor)
	unsetenv(t, "TMUX")

	dir := t.TempDir()
	mockPath := mock.Path{Missing: []string{editor}}

	spyRunner := &spy.SpyRunner{}

	err := Run([]string{dir}, "auto", spyRunner, &mockPath)
	if !errors.Is(err, ErrEditorNotInstalled) {
		t.Fatalf("got=%v, want=%v", err, ErrEditorNotInstalled)
	}
	requireCalls(t, nil, spyRunner.Calls)
}

func TestHerdrFolderFromPrompt(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("TMUX", "outer-tmux")
	t.Setenv("EDITOR", editor)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "project")
	requireNoError(t, os.Mkdir(dir, 0755))
	runner := &spy.SpyRunner{Responses: []spy.Response{
		{}, {}, {OnRun: mock.WriteToStdout("project")},
		{OnRun: mock.WriteToStdout(`{"result":{"type":"workspace_list","workspaces":[]}}`)},
		{OnRun: mock.WriteToStdout(`{"result":{"workspace":{"workspace_id":"w1"}}}`)},
		{OnRun: mock.WriteToStdout(`{"result":{"type":"ok"}}`)},
	}}
	err := Run(nil, "auto", runner, mock.Path{Missing: []string{"tmux"}})
	requireNoError(t, err)
	requireCalls(t, [][]string{
		{"fd", "--max-depth", "2", "--follow", "--hidden", "--exclude", "{.git,node_modules,Library}", ".", "--base-directory", home},
		{"fd", "--min-depth", "3", "--follow", "--hidden", "--exclude", "{.git,node_modules,Library}", ".", "--base-directory", home},
		{"fzf", "--reverse", "--height", "70%", "--tmux", "70%", "--no-sort"},
		{"herdr", "workspace", "list"},
		{"herdr", "workspace", "create", "--cwd", dir, "--label", filepath.Base(dir), "--no-focus"},
		{"herdr", "workspace", "focus", "w1"},
	}, runner.Calls)
}

func TestHerdrRelativeFileInRepository(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("EDITOR", "editor -R")
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	requireNoError(t, os.Mkdir(subdir, 0755))
	createFile(t, subdir, "file.txt")
	t.Chdir(dir)
	runner := &spy.SpyRunner{Responses: []spy.Response{
		{OnRun: mock.WriteToStdout(dir)},
		{OnRun: mock.WriteToStdout(`{"result":{"type":"workspace_list","workspaces":[]}}`)},
		{OnRun: mock.WriteToStdout(`{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"w1:t1"},"root_pane":{"pane_id":"w1:p1"}}}`)},
		{OnRun: mock.WriteToStdout(`{"result":{"type":"ok"}}`)},
		{OnRun: mock.WriteToStdout(`{"result":{"type":"ok"}}`)},
		{OnRun: mock.WriteToStdout(`{"result":{"type":"ok"}}`)},
	}}
	requireNoError(t, Run([]string{"sub/file.txt"}, "auto", runner, mock.Path{Missing: []string{"tmux"}}))
	requireCalls(t, [][]string{
		{"git", "-C", "sub", "rev-parse", "--show-toplevel"},
		{"herdr", "workspace", "list"},
		{"herdr", "workspace", "create", "--cwd", dir, "--label", filepath.Base(dir), "--no-focus"},
		{"herdr", "pane", "run", "w1:p1", "'editor' '-R' '" + filepath.Join(dir, "sub/file.txt") + "'"},
		{"herdr", "tab", "focus", "w1:t1"},
		{"herdr", "workspace", "focus", "w1"},
	}, runner.Calls)
}

func TestRexFolderFromPrompt(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("REX_SESSION", "session:outer")
	t.Setenv("EDITOR", editor)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "project")
	requireNoError(t, os.Mkdir(dir, 0755))
	runner := &spy.SpyRunner{Responses: []spy.Response{
		{}, {}, {OnRun: mock.WriteToStdout("project")},
		{OnRun: mock.WriteToStdout(`{"sessions":[]}`)},
		{OnRun: mock.WriteToStdout(`{"session_id":"session:1"}`)},
		{},
	}}
	requireNoError(t, Run(nil, "auto", runner, mock.Path{Missing: []string{"tmux", "herdr"}}))
	requireCalls(t, [][]string{
		{"fd", "--max-depth", "2", "--follow", "--hidden", "--exclude", "{.git,node_modules,Library}", ".", "--base-directory", home},
		{"fd", "--min-depth", "3", "--follow", "--hidden", "--exclude", "{.git,node_modules,Library}", ".", "--base-directory", home},
		{"fzf", "--reverse", "--height", "70%", "--tmux", "70%", "--no-sort"},
		{"rex", "ls", "--json"},
		{"rex", "new", project.Name(dir), "--cwd", dir, "--json"},
		{"rex", "do", "session.select", "session_id=session:1"},
	}, runner.Calls)
}

func TestRexRelativeFileInRepository(t *testing.T) {
	t.Setenv("HERDR_ENV", "1") // Explicit Rex overrides the detected host.
	t.Setenv("EDITOR", "editor -R")
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	requireNoError(t, os.Mkdir(subdir, 0755))
	file := createFile(t, subdir, "a'b.txt")
	t.Chdir(subdir)
	runner := &spy.SpyRunner{Responses: []spy.Response{
		{OnRun: mock.WriteToStdout(dir)},
		{OnRun: mock.WriteToStdout(`{"sessions":[{"session_id":"session:1","label":"` + project.Name(dir) + `"}]}`)},
		{}, {},
	}}
	requireNoError(t, Run([]string{"a'b.txt"}, "rex", runner, mock.Path{Missing: []string{"tmux", "herdr"}}))
	requireCalls(t, [][]string{
		{"git", "-C", ".", "rev-parse", "--show-toplevel"},
		{"rex", "ls", "--json"},
		{"rex", "run", "--session", "session:1", "--cwd", dir, "--", "editor", "-R", file},
		{"rex", "do", "session.select", "session_id=session:1"},
	}, runner.Calls)
}
