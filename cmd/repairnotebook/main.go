package main

import (
	"fmt"
	"io"
	"os"

	"github.com/tmc/nbsim/notebooks"
)

func main() {
	err, ret := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	os.Exit(ret)
}

func run() (error, int) {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err, 1
	}
	repairedNotebook, ok := notebooks.RepairNotebookJSON(string(in))
	fmt.Println(repairedNotebook)
	if !ok {
		return nil, 1
	}
	return nil, 0
}
