// Command moolah is a personal finance tool for the command line.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"

	"moolah/app"
	"moolah/cli"
)

func main() {
	paths, err := app.DefaultPaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
	env := cli.Env{
		Getenv:       os.Getenv,
		Interactive:  term.IsTerminal(int(os.Stdin.Fd())),
		Now:          time.Now,
		OpenBrowser:  openBrowser,
		Paths:        paths,
		ReadPassword: readPassword,
		RunEditor:    runEditor,
		Stderr:       os.Stderr,
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
	}
	os.Exit(cli.Run(env, os.Args[1:]))
}

func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

func runEditor(path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	parts := strings.Fields(editor)
	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func openBrowser(url string) error {
	cmd := exec.Command("xdg-open", url)
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Start()
}
