package cli

import (
	"errors"
	"fmt"
	"os"
)

// editLoop opens text in $EDITOR and calls save with the result. When save fails,
// it shows the error and offers to edit again, like "crontab -e".
func (g *globals) editLoop(text, pattern string, save func(string) error) error {
	if g.env.RunEditor == nil {
		return errors.New("no editor available")
	}
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	defer os.Remove(name)
	for {
		if err := os.WriteFile(name, []byte(text), 0o600); err != nil {
			return err
		}
		if err := g.env.RunEditor(name); err != nil {
			return fmt.Errorf("editor: %w", err)
		}
		b, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		text = string(b)
		if err := save(text); err == nil {
			return nil
		} else {
			fmt.Fprintln(g.env.Stderr, "error: "+err.Error())
		}
		again, err := g.in.confirm("Edit again?")
		if err != nil {
			return err
		}
		if !again {
			return errors.New("not saved")
		}
	}
}
