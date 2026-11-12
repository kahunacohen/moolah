package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"moolah/app"
)

func init() {
	extraCommands = append(extraCommands, historyCmd)
}

type historyJSON struct {
	Changes  []historyEntryJSON `json:"changes"`
	Command  string             `json:"command"`
	Computer string             `json:"computer"`
	ID       int64              `json:"id"`
	Time     time.Time          `json:"time"`
}

type historyEntryJSON struct {
	Details []string `json:"details"`
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Op      string   `json:"op"`
}

func historyCmd(g *globals) *cobra.Command {
	var limit int
	var txID int64
	cmd := &cobra.Command{
		Use:   "history [action-id]",
		Short: "What changed in the book, when, and with which command",
		Long: "history lists your actions, newest first: each command or change in the browser\n" +
			"that changed the book. Give an action's ID to see exactly what it changed.\n" +
			"The history moves with sync and backups. To delete old history: moolah history prune.",
		Example: "  moolah history\n  moolah history 812\n  moolah history --tx 42",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := app.HistoryParams{Limit: limit, Transaction: txID}
			if len(args) == 1 {
				id, err := strconv.ParseInt(args[0], 10, 64)
				if err != nil || id <= 0 {
					return fmt.Errorf("%q is not an action ID. List actions with: moolah history", args[0])
				}
				p = app.HistoryParams{ID: id}
			}
			return g.withBook(func(a *app.App) error {
				h, err := a.History(cmd.Context(), p)
				if err != nil {
					return err
				}
				var js []historyJSON
				for _, act := range h {
					j := historyJSON{Command: act.Command, Computer: act.Machine, ID: act.ID, Time: act.Time, Changes: []historyEntryJSON{}}
					for _, e := range act.Entries {
						j.Changes = append(j.Changes, historyEntryJSON{Details: e.Details, Kind: string(e.Kind), Name: e.Name, Op: string(e.Op)})
					}
					js = append(js, j)
				}
				if g.format == "json" {
					return printJSON(g.out(), js)
				}
				if g.format == "table" && (p.ID != 0 || p.Transaction != 0) {
					for i, act := range h {
						if i > 0 {
							fmt.Fprintln(g.out())
						}
						printAction(g.out(), act)
					}
				} else if len(h) > 0 || g.format != "table" {
					t := &table{headers: []string{"ID", "Time", "Computer", "Command", "Changes"}, right: map[int]bool{0: true}}
					for _, act := range h {
						t.add(strconv.FormatInt(act.ID, 10), localTime(act.Time), act.Machine, orUnknown(act.Command), act.Summary())
					}
					if err := g.render(t, js); err != nil {
						return err
					}
				}
				if g.format != "table" {
					return nil
				}
				if len(h) == 0 {
					fmt.Fprintln(g.out(), "No history yet.")
				}
				if n, err := a.UnrecordedChanges(cmd.Context()); err != nil {
					return err
				} else if n > 0 {
					fmt.Fprintf(g.out(), "\n%s made outside moolah (for example with sqlite3) %s no time or command.\n",
						count(n, "change was", "changes were"), plural(n, "has", "have"))
				}
				return nil
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "show this many actions (0 = all)")
	cmd.Flags().Int64Var(&txID, "tx", 0, "only actions that changed this transaction")

	var before string
	var yes bool
	prune := &cobra.Command{
		Use:   "prune --before DATE",
		Short: "Delete the history of actions before a date (your data does not change)",
		Long: "prune deletes old history, for example to really remove what you deleted.\n" +
			"Your accounts, transactions and settings do not change. With sync, your other\n" +
			"computers delete the same history.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if before == "" {
				return errors.New("give the date: moolah history prune --before YYYY-MM-DD")
			}
			d, err := parseDateFlag(before)
			if err != nil {
				return err
			}
			if !yes {
				ok, err := g.in.confirm(fmt.Sprintf("Delete the history of all actions before %s? This cannot be undone", d))
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("nothing deleted (use --yes to confirm without a prompt)")
				}
			}
			return g.withBook(func(a *app.App) error {
				n, err := a.PruneHistory(cmd.Context(), d)
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Deleted the history of %s before %s.\n", count(int(n), "action", "actions"), d)
				return nil
			})
		},
	}
	prune.Flags().StringVar(&before, "before", "", "delete history before this date (YYYY-MM-DD)")
	prune.Flags().BoolVar(&yes, "yes", false, "delete without asking")
	cmd.AddCommand(prune)
	return cmd
}

func printAction(w io.Writer, act app.HistoryAction) {
	fmt.Fprintf(w, "Action %d  %s  on %s\n", act.ID, localTime(act.Time), act.Machine)
	fmt.Fprintf(w, "  %s\n", orUnknown(act.Command))
	for _, e := range act.Entries {
		fmt.Fprintf(w, "\n  %s %s %s\n", e.Kind, e.Name, e.Op)
		for _, d := range e.Details {
			fmt.Fprintf(w, "    %s\n", d)
		}
	}
}

func localTime(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }

func orUnknown(cmd string) string {
	if cmd == "" {
		return "(unknown command)"
	}
	return cmd
}
