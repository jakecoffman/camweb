package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// hmm gomaxprocs 1 causes it to fail
	//runtime.GOMAXPROCS(1)

	log.SetFlags(log.Lshortfile | log.LstdFlags)
	go serveHTTP()
	go serveStreams()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigs
	log.Println("Got signal", sig)
}
