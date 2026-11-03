package rex

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/eskelinenantti/zipln/internal/project"
	"github.com/eskelinenantti/zipln/internal/test/mock"
	"github.com/eskelinenantti/zipln/internal/test/spy"
	"github.com/google/go-cmp/cmp"
)

const empty = `{"sessions":[]}`
const existing = `{"sessions":[{"session_id":"session:1","label":"project-abcd"}]}`
const created = `{"session_id":"session:1","initial_windows":[{"window_id":"window:1","block_ids":["block:1"]}]}`

func responses(outputs ...string) []spy.Response {
	var result []spy.Response
	for _, output := range outputs {
		result = append(result, spy.Response{OnRun: mock.WriteToStdout(output)})
	}
	return result
}

func TestOpen(t *testing.T) {
	for _, tt := range []struct {
		name    string
		command []string
		outputs []string
		calls   [][]string
	}{
		{"new folder", nil, []string{empty, created, ""}, [][]string{
			{"rex", "ls", "--json"},
			{"rex", "new", "project-abcd", "--cwd", "/project dir", "--json"},
			{"rex", "do", "session.select", "session_id=session:1"},
		}},
		{"existing folder", nil, []string{existing, ""}, [][]string{
			{"rex", "ls", "--json"},
			{"rex", "do", "session.select", "session_id=session:1"},
		}},
		{"file in new session", []string{"editor", "-R", "/project dir/a'b; $(touch bad)"}, []string{empty, created, ""}, [][]string{
			{"rex", "ls", "--json"},
			{"rex", "new", "project-abcd", "--cwd", "/project dir", "--json", "--", "editor", "-R", "/project dir/a'b; $(touch bad)"},
			{"rex", "do", "session.select", "session_id=session:1"},
		}},
		{"file in existing session", []string{"editor", "-R", "/project dir/a'b; $(touch bad)"}, []string{existing, "", ""}, [][]string{
			{"rex", "ls", "--json"},
			{"rex", "run", "--session", "session:1", "--cwd", "/project dir", "--", "editor", "-R", "/project dir/a'b; $(touch bad)"},
			{"rex", "do", "session.select", "session_id=session:1"},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &spy.SpyRunner{Responses: responses(tt.outputs...)}
			err := (Cmd{Runner: runner}).Open(project.Project{Name: "project-abcd", WorkingDir: "/project dir"}, tt.command)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.calls, runner.Calls); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestInvalidResponsesStopWorkflow(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		outputs    []string
	}{
		{"invalid JSON", "decode rex response", []string{"not json"}},
		{"missing sessions", "invalid rex session list", []string{`{}`}},
		{"null list", "invalid rex session list", []string{`null`}},
		{"missing session id", "session has no id", []string{`{"sessions":[{"label":"project-abcd"}]}`}},
		{"duplicate label", "multiple rex sessions", []string{`{"sessions":[{"label":"project-abcd","session_id":"session:1"},{"label":"project-abcd","session_id":"session:2"}]}`}},
		{"invalid creation JSON", "decode rex response", []string{empty, "not json"}},
		{"missing created id", "no session id", []string{empty, `{}`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &spy.SpyRunner{Responses: responses(tt.outputs...)}
			err := (Cmd{Runner: runner}).Open(project.Project{Name: "project-abcd"}, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) || len(runner.Calls) != len(tt.outputs) {
				t.Fatalf("got %v, calls %v", err, runner.Calls)
			}
		})
	}
}

func TestCommandFailuresStopWorkflow(t *testing.T) {
	failure := errors.New("command failed")
	for _, list := range []string{empty, existing} {
		for i := range 3 {
			t.Run(fmt.Sprintf("%s step %d", list, i), func(t *testing.T) {
				runner := &spy.SpyRunner{Responses: responses(list, created, "")}
				runner.Responses[i] = spy.Response{OnRun: func(cmd *exec.Cmd) error {
					fmt.Fprint(cmd.Stderr, "server or client unavailable")
					return failure
				}}
				err := (Cmd{Runner: runner}).Open(project.Project{Name: "project-abcd"}, []string{"editor", "file"})
				if !errors.Is(err, failure) || !strings.Contains(err.Error(), "server or client unavailable") || len(runner.Calls) != i+1 {
					t.Fatalf("got %v, calls %v", err, runner.Calls)
				}
				if i == 2 && !strings.Contains(err.Error(), "REX_CLIENT") {
					t.Fatalf("missing client guidance: %v", err)
				}
			})
		}
	}
}
