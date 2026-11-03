package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/eskelinenantti/zipln/internal/multiplexer"
	"github.com/eskelinenantti/zipln/internal/picker"
	"github.com/eskelinenantti/zipln/internal/project"
	"github.com/eskelinenantti/zipln/internal/shell"
	"github.com/eskelinenantti/zipln/internal/shell/path"
	"github.com/eskelinenantti/zipln/internal/shell/runner"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "zipln [file|folder]",
	Short: "zipln opens herdr workspaces based on files and folders.",
	Args:  cobra.MaximumNArgs(1),
	Long: `zipln creates or switches to herdr workspaces based on files and folders.

Pick a location from the fuzzy finder. A herdr workspace will be created
(or reused) for that location automatically. tmux and Rex sessions are also supported.

When a file is selected or passed as an argument, zipln opens it in
$EDITOR and creates the workspace for the repository root, or the file's
directory if it is not inside a git repository.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mux, err := cmd.Flags().GetString("mux")
		if err != nil {
			return err
		}
		return Run(args, mux, runner.CmdRunner{}, path.Path{})
	},
}

var helpNoEditorConfigured = `
No editor was configured. Specify the editor you would like to use by setting the $EDITOR variable.
For example, to use Vim as your editor, add the following line to your ~/.zshrc or ~/.bashrc:
				
export EDITOR=vim`

func installHelp(commands []string, shellPath path.ShellPath) string {
	if shellPath.Contains("brew") {
		return "\nTo install, run:\n\n  brew install " + strings.Join(commands, " ") + "\n"
	}
	return "\nInstall the missing tools with your package manager and make sure they're on $PATH."
}

var ErrEditorNotInstalled = errors.New("editor not installed")
var ErrEditorEnvNotSet = errors.New("editor not configured")

func Run(args []string, muxFlag string, runner runner.Runner, path path.ShellPath) error {
	mux, err := multiplexer.Select(muxFlag, path, runner)
	if err != nil {
		return err
	}
	shell, err := shell.Init(path, runner)
	if err != nil {
		return err
	}

	editorCmd, err := editorCmd(path)
	if err != nil {
		return err
	}

	var target string
	if len(args) == 0 {
		target, err = picker.Prompt(shell.Fd, shell.Fzf)
	} else {
		target = args[0]
	}

	if target == "" || err != nil {
		return err
	}

	isDir, err := isDir(target)
	if err != nil {
		return err
	}

	var proj project.Project
	if isDir {
		proj, err = project.ForDir(target)
	} else {
		proj, err = project.ForFile(target, shell.Git)
	}

	if err != nil {
		return fmt.Errorf("could not edit %s: %w", target, err)
	}

	var command []string
	if !isDir {
		// The editor starts in the project root, which may differ from our cwd.
		file, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		command = append(editorCmd, file)
	}

	return mux.Open(proj, command)
}

func isDir(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, project.ErrInvalidPath
	}

	return fileInfo.IsDir(), nil
}

func Execute() {
	if code := execute(rootCmd); code != 0 {
		os.Exit(code)
	}
}

func execute(cmd *cobra.Command) int {
	cmd.SilenceUsage = true
	cmd.SilenceErrors = false
	err := cmd.Execute()

	if err == nil {
		return 0
	}

	if errors.Is(err, ErrEditorEnvNotSet) {
		cmd.PrintErrln(helpNoEditorConfigured)
	}

	var commandNotInstalled shell.NotInstalledError
	var missingTools shell.MissingToolsError
	if errors.As(err, &missingTools) {
		cmd.PrintErrln(installHelp(missingTools.Tools, path.Path{}))
	} else if errors.As(err, &commandNotInstalled) {
		cmd.PrintErrln(installHelp([]string{commandNotInstalled.Cmd}, path.Path{}))
	}

	waitForEnter(cmd.InOrStdin(), cmd.ErrOrStderr())
	return 1
}

func waitForEnter(input io.Reader, output io.Writer) {
	fmt.Fprint(output, "\nPress Enter to close.")
	_, _ = bufio.NewReader(input).ReadString('\n')
}

func editorCmd(path path.ShellPath) ([]string, error) {
	editorCmd := strings.Fields(os.Getenv("EDITOR"))

	if len(editorCmd) == 0 {
		return nil, ErrEditorEnvNotSet
	}

	if !path.Contains(editorCmd[0]) {
		return nil, ErrEditorNotInstalled
	}
	return editorCmd, nil
}

func init() {
	rootCmd.Flags().String("mux", "auto", "Terminal host override (auto, tmux, herdr, rex)")
}
