package tui

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shr-go/bili_live_tui/pkg/logging"
	"golang.org/x/term"
)

// Bubble Tea v0.22.1 restores its console before cancelreader.Close runs.
// On Windows that final Close can restore the reader's saved *raw* mode again.
// Restore our startup modes after all library defers, for both login and chat.
func RunProgram(program *tea.Program) (tea.Model, error) {
	inputFD, outputFD := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	inputState, inputErr := term.GetState(inputFD)
	outputState, outputErr := term.GetState(outputFD)
	defer func() {
		if inputErr == nil {
			if err := term.Restore(inputFD, inputState); err != nil {
				logging.Errorf("restore input terminal: %v", err)
			}
		}
		if outputErr == nil {
			if err := term.Restore(outputFD, outputState); err != nil {
				logging.Errorf("restore output terminal: %v", err)
			}
		}
	}()
	return program.StartReturningModel()
}
