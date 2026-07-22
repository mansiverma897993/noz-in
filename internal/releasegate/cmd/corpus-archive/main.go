package main

import (
	"fmt"
	"os"

	"github.com/mansiverma897993/noz-in/internal/releasegate"
)

func main() {
	if len(os.Args) != 3 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: corpus-archive ARCHIVE DESTINATION")
		os.Exit(2)
	}
	if err := releasegate.VerifyAndExtractCorpus(os.Args[1], os.Args[2]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "verify release corpus: %s\n", err)
		os.Exit(1)
	}
}
