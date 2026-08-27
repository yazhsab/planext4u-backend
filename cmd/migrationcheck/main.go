package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yazhsab/planext4u-backend/internal/migrations"
)

func main() {
	root := flag.String("root", "migrations", "migration root")
	flag.Parse()
	items, err := migrations.Discover(*root)
	if err == nil {
		err = migrations.Validate(items)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("validated %d service-owned migrations\n", len(items))
}
