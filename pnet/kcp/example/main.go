package main

import (
	"fmt"
	"io"
	"log"
	"time"

	"github.com/nkbai/goice/pnet/kcp"
)

func main() {

	if listener, err := ListenWithOptions("127.0.0.1:12346", 10, 3); err == nil {
		// spin-up the client
		go client()

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

func client() {

	// wait for server to become ready
	time.Sleep(time.Second)

	// dial to the echo server
	if sess, err := kcp.DialWithOptions("127.0.0.1:22345", 10, 3); err == nil {

		for {
			data := time.Now().String()
			buf := make([]byte, len(data))
			log.Println("sent:", data)
			fmt.Println("sent:", data)
			if _, err := sess.Write([]byte(data)); err == nil {
				// read back the data
				if _, err := io.ReadFull(sess, buf); err == nil {
					fmt.Println("recv:", string(buf))
				} else {
					log.Fatal(err)
				}
			} else {
				log.Fatal(err)
				fmt.Println(err)
			}
			time.Sleep(time.Second)
		}
	} else {
		log.Fatal(err)
	}
}
