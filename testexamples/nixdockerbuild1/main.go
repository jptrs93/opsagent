package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	fmt.Printf("nixdockerbuild1 env OPENDEPLOY_E2E_MESSAGE=%s\n", os.Getenv("OPENDEPLOY_E2E_MESSAGE"))
	fmt.Printf("nixdockerbuild1 env OPENDEPLOY_E2E_COLOR=%s\n", os.Getenv("OPENDEPLOY_E2E_COLOR"))

	for count := 1; ; count++ {
		fmt.Printf("nixdockerbuild1 count=%d time=%s\n", count, time.Now().Format(time.RFC3339))
		time.Sleep(10 * time.Second)
	}
}
