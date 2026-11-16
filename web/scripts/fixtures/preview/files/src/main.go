package main

import (
	"fmt"
	"os"
)

// main prints its arguments.
func main() {
	for i, a := range os.Args[1:] {
		fmt.Printf("%d: %q\n", i, a)
	}
}
