package main

import (
	"github.com/jakecoffman/camweb"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log.SetFlags(log.Lshortfile | log.LstdFlags)
	go camweb.ServeHTTP()
	go camweb.ServeStreams()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigs
	log.Println("Got signal", sig)
}
