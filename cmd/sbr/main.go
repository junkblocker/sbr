package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/junkblocker/sbr/processor"
)

var debugFlag = flag.Uint("d", 0, "Be more verbose")

func main() {
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Println("Usage: sbr [-d 0,1,2,3] <file_or_directory_path> <output_dir>")
		os.Exit(1)
	}

	opts := processor.Options{DebugLevel: *debugFlag}

	inPath := flag.Arg(0)
	outPath := flag.Arg(1)

	inPathInfo, err := os.Stat(inPath)
	if err != nil {
		log.Fatalf("Error accessing path %s: %v\n", inPath, err)
	}

	outPathInfo, err := os.Stat(outPath)
	if err != nil {
		if err = os.Mkdir(outPath, 0755); err != nil {
			log.Fatalf("Error creating directory %s: %v\n", outPath, err)
		}
	} else if !outPathInfo.IsDir() {
		log.Fatalf("Output path %s is not a directory\n", outPath)
	}

	var wg sync.WaitGroup
	if inPathInfo.IsDir() {
		processor.ProcessDirectory(&wg, inPath, outPath, opts)
	} else {
		processor.ProcessFileFromPath(inPath, outPath, opts)
	}
	wg.Wait()
}
