package cli

import (
	"fmt"

	"github.com/buildoak/wet/stats"
)

func RunStatusline() error {
	line, err := stats.RenderStatusline()
	if err != nil {
		return nil // silent failure
	}
	if line != "" {
		fmt.Println(line)
	}
	return nil
}
