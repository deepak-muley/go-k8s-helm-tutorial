package main

import (
	"fmt"
	"os"
)

func main() {
	helmClient := NewHelmClient()
	releases, err := helmClient.ListReleases("default", "")
	if err != nil {
		fmt.Printf("Failed to get releaes : %s", err)
		os.Exit(-1)
	}
	fmt.Printf("%#v", releases)
}
