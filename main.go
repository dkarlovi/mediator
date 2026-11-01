package main

import (
	"os"

	"github.com/dkarlovi/mediator/commands"
	"github.com/symfony-cli/console"
)

var (
	// version is overridden at linking time
	version = "dev"
	// buildDate is overridden at linking time
	buildDate string
)

func main() {
	app := &console.Application{
		Name:      "mediator",
		Usage:     "Reconcile media files with your preferred containers and codecs",
		Version:   version,
		BuildDate: buildDate,
		Channel:   "stable",
		Flags: []console.Flag{
			&console.StringFlag{
				Name:    "config",
				Aliases: []string{"C"},
				Usage: `Path to configuration file

Lookup order (first existing path wins):
  1) <info>--config <path></> (explicit)
  2) <info>./mediator.yaml</> (current working directory)
  3) user config dir:
     Linux:   <info>$XDG_CONFIG_HOME/mediator/mediator.yaml</>, else <info>~/.config/mediator/mediator.yaml</>
     Windows: <info>%AppData%\mediator\mediator.yaml</>
     macOS:   <info>~/Library/Application Support/mediator/mediator.yaml</>`,
			},
		},
		Commands: commands.Commands(),
	}

	app.Run(os.Args)
}
