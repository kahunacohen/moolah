package cli

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"moolah/app"
	"moolah/web"
)

func init() {
	extraCommands = append(extraCommands, serveCmd)
}

func serveCmd(g *globals) *cobra.Command {
	var port int
	var newToken, noBrowser bool
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
			ln, err := g.listen(port, cmd.Flags().Changed("port"))
			if err != nil {
				return err
			}
			token, err := g.env.Paths.ServeToken(newToken)
			if err != nil {
				_ = ln.Close()
				return err
			}
			if newToken {
				fmt.Fprintln(g.out(), "New sign-in token: browsers signed in before must open the address below again.")
			}
			books, _ := g.env.Paths.Books()
			s, err := web.New(a, web.Options{Addr: ln.Addr().String(), Books: len(books), Token: token})
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
	cmd.Flags().IntVar(&port, "port", 0, fmt.Sprintf("port to listen on (default: the serve_port setting, or %d)", app.DefaultServePort))
	cmd.Flags().BoolVar(&newToken, "new-token", false, "replace the sign-in token; signs out every browser")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a browser")
	return cmd
}

// listen opens the serve port: the --port flag, else the serve_port setting, else
// the default. Only a port the user typed is required; a busy configured or
// default port falls back to a free one, so a second book can be served at once.
func (g *globals) listen(flagPort int, flagGiven bool) (net.Listener, error) {
	if flagGiven {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", flagPort))
		if err != nil {
			return nil, fmt.Errorf("cannot listen on port %d: %w. Choose another with --port", flagPort, err)
		}
		return ln, nil
	}
	c, err := g.env.Paths.LoadConfig()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", c.Port()))
	if err == nil {
		return ln, nil
	}
	ln, err2 := net.Listen("tcp", "127.0.0.1:0")
	if err2 != nil {
		return nil, err2
	}
	fmt.Fprintf(g.env.Stderr, "warning: port %d is busy (%v), using %d\n", c.Port(), err, ln.Addr().(*net.TCPAddr).Port)
	return ln, nil
}
