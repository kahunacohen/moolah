package cli

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
)

// prompter asks questions on stderr and reads answers from stdin.
type prompter struct {
	env Env
	r   *bufio.Reader
}

func newPrompter(env Env) *prompter { return &prompter{env: env, r: bufio.NewReader(env.Stdin)} }

// ask prints a question with a default and returns the answer (or the default).
func (p *prompter) ask(question, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.env.Stderr, "%s [%s]: ", question, def)
	} else {
		fmt.Fprintf(p.env.Stderr, "%s: ", question)
	}
	line, err := p.r.ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no answer (input ended)")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// confirm asks a yes/no question. Without a terminal it returns false.
func (p *prompter) confirm(question string) (bool, error) {
	if !p.env.Interactive {
		return false, nil
	}
	ans, err := p.ask(question+" [y/N]", "")
	if err != nil {
		return false, err
	}
	ans = strings.ToLower(ans)
	return ans == "y" || ans == "yes", nil
}

// passphrase returns MOOLAH_PASSPHRASE, or asks for one (twice when confirm is true).
func (p *prompter) passphrase(confirm bool) (string, error) {
	if v := p.env.Getenv("MOOLAH_PASSPHRASE"); v != "" {
		return v, nil
	}
	if !p.env.Interactive || p.env.ReadPassword == nil {
		return "", errors.New("a passphrase is needed: run in a terminal, set MOOLAH_PASSPHRASE, or set backup_recipient")
	}
	pass, err := p.env.ReadPassword("Passphrase: ")
	if err != nil {
		return "", err
	}
	if confirm {
		again, err := p.env.ReadPassword("Passphrase again: ")
		if err != nil {
			return "", err
		}
		if again != pass {
			return "", errors.New("the passphrases do not match")
		}
	}
	return pass, nil
}
