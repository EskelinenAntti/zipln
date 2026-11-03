package rex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/eskelinenantti/zipln/internal/project"
	"github.com/eskelinenantti/zipln/internal/shell/runner"
)

type Cmd struct {
	runner.Runner
}

func (r Cmd) output(args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("rex", args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := r.Run(cmd); err != nil {
		return nil, fmt.Errorf("rex %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (r Cmd) call(result any, args ...string) error {
	stdout, err := r.output(args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(stdout, result); err != nil {
		return fmt.Errorf("decode rex response: %w", err)
	}
	return nil
}

func (r Cmd) Open(project project.Project, command []string) error {
	var list struct {
		Sessions []struct {
			ID    string `json:"session_id"`
			Label string `json:"label"`
		} `json:"sessions"`
	}
	if err := r.call(&list, "ls", "--json"); err != nil {
		return err
	}
	if list.Sessions == nil {
		return fmt.Errorf("invalid rex session list response")
	}
	var id string
	for _, session := range list.Sessions {
		if session.Label != project.Name {
			continue
		}
		if session.ID == "" {
			return fmt.Errorf("rex session has no id")
		}
		if id != "" {
			return fmt.Errorf("multiple rex sessions named %q", project.Name)
		}
		id = session.ID
	}
	if id == "" {
		args := []string{"new", project.Name, "--cwd", project.WorkingDir, "--json"}
		if len(command) > 0 {
			args = append(args, "--")
			args = append(args, command...)
		}
		var created struct {
			ID string `json:"session_id"`
		}
		if err := r.call(&created, args...); err != nil {
			return err
		}
		id = created.ID
		if id == "" {
			return fmt.Errorf("rex new response has no session id")
		}
	} else if len(command) > 0 {
		// Rex accepts argv directly; shell metacharacters in paths stay literal.
		args := []string{"run", "--session", id, "--cwd", project.WorkingDir, "--"}
		if _, err := r.output(append(args, command...)...); err != nil {
			return err
		}
	}
	// Session creation doesn't select it in the app. Rex uses REX_CLIENT when
	// set, or the sole connected app; it reports ambiguous clients as an error.
	if _, err := r.output("do", "session.select", "session_id="+id); err != nil {
		return fmt.Errorf("cannot select Rex session; open the Rex app and set REX_CLIENT if multiple apps are connected: %w", err)
	}
	return nil
}
