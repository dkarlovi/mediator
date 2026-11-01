package commands

import (
	"context"
	"fmt"

	"github.com/dkarlovi/mediator/media"
	"github.com/dkarlovi/mediator/plan"
	"github.com/dkarlovi/mediator/transcode"
	"github.com/symfony-cli/console"
	"github.com/symfony-cli/terminal"
)

var probeCmd = &console.Command{
	Name:        "probe",
	Usage:       "Show the streams of a file and the plan mediator would apply",
	Description: "With <info>-v</>, also prints the exact ffmpeg command mediator would run.",
	Args: []*console.Arg{
		{Name: "file", Description: "Media file to inspect"},
	},
	Action: func(c *console.Context) error {
		cfg, err := loadConfig(c)
		if err != nil {
			return err
		}
		f := c.Args().Get("file")
		info, err := media.Probe(context.Background(), cfg.FFprobe, f)
		if err != nil {
			return console.Exit(err.Error(), 1)
		}
		p, err := plan.Build(info, cfg)
		if err != nil {
			return console.Exit(err.Error(), 1)
		}
		printPlan(c.App.Writer, p, "")
		if !p.NeedsWork() {
			fmt.Fprintln(c.App.Writer, "\n<info>Already matches the config.</>")
			return nil
		}
		if terminal.IsVerbose() {
			fmt.Fprintf(c.App.Writer, "\n%s", cfg.FFmpeg)
			for _, a := range transcode.Args(p, transcode.TempPath(p)) {
				fmt.Fprintf(c.App.Writer, " %s", quote(a))
			}
			fmt.Fprintln(c.App.Writer)
		}
		return nil
	},
}

func quote(s string) string {
	for _, r := range s {
		if r == ' ' || r == '\'' || r == '"' || r == '\\' || r == '(' || r == ')' {
			return fmt.Sprintf("%q", s)
		}
	}
	return s
}
