package main

import (
	"fmt"
	"log"
)

func main() {

	if listener, err := ListenWithOptions("127.0.0.1:12346", 10, 3); err == nil {

		for {
			s, err := listener.AcceptKCP()
			if err != nil {
				log.Fatal(err)
			}
			go handleEcho(s)
			
		}
		select {}
	} else {
		log.Fatal(err)
	}
}

// handleEcho send back everything it received
func handleEcho(conn *PeerSession) {
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			log.Println(err)
			return
		}

		n, err = conn.Write(buf[:n])
		fmt.Println("echo:", string(buf[:n]))
		if err != nil {
			log.Println(err)
			return
		}
	}
}
