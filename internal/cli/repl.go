package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// repl runs a read-eval-print loop. It prints banner, then for each non-empty
// line of input it prints a prompt and hands the trimmed line to turn. The loop
// ends on EOF or when the user types "exit". This single implementation is
// shared by every interactive mode so the loop logic lives in exactly one place.
func repl(in io.Reader, out io.Writer, banner string, turn func(line string)) error {
	if banner != "" {
		fmt.Fprintln(out, banner)
	}

	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "\n> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" {
			break
		}
		turn(line)
	}
	return scanner.Err()
}
