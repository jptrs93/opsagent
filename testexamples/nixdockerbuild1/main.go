package main

import (
	"fmt"
	"time"
)

func main() {
	for count := 1; ; count++ {
		fmt.Printf("nixdockerbuild1 count=%d time=%s\n", count, time.Now().Format(time.RFC3339))
		time.Sleep(10 * time.Second)
	}
}
