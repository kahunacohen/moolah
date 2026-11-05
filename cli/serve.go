package cli

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"moolah/web"
)

func init() {
	extraCommands = append(extraCommands, serveCmd)
}

func serveCmd(g *globals) *cobra.Command {
	var port int
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Open the browser UI (only reachable from this computer; Ctrl+C stops it)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			defer a.Close()
			if !g.noSync {
				warnings, err := a.SyncBefore(cmd.Context())
				for _, w := range warnings {
					fmt.Fprintln(g.env.Stderr, "warning: "+w)
				}
				if err != nil {
					_ = a.ReleaseLock(cmd.Context())
					return err
				}
			}
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				return fmt.Errorf("cannot listen on port %d: %w", port, err)
			}
			books, _ := g.env.Paths.Books()
			s, err := web.New(a, ln.Addr().String(), len(books))
			if err != nil {
				_ = ln.Close()
				return err
			}
			s.NoSync = g.noSync
			fmt.Fprintf(g.out(), "moolah running at %s   (Ctrl+C to stop)\n", s.URL())
			if !noBrowser && g.env.OpenBrowser != nil {
				if err := g.env.OpenBrowser(s.URL()); err != nil {
					fmt.Fprintln(g.env.Stderr, "warning: could not open a browser; open the address above yourself")
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()
			err = s.Run(ctx, ln)
			fmt.Fprintln(g.out(), "Stopped.")
			return err
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "port to listen on (default: a free port)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a browser")
	return cmd
}
