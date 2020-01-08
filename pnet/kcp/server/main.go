package main

import (
	"crypto/sha1"
	"fmt"
	"log"
	"time"

	"golang.org/x/crypto/pbkdf2"

	"github.com/nkbai/goice/pnet/kcp"
)

func main() {

	listener, _ := kcp.NewListenPeer(nil, 10, 3)
	listener.ListenWithOptions("127.0.0.1:14567")
	for {
		s, err := listener.AcceptKCP()
		if err != nil {
			log.Fatal(err)
		}
		go handleEcho(s)
		// d, err := listener.DialWithOptions("127.0.0.1:4823", block, 10, 3)
		// go handlClient(d)

	}
	select {}

}

// handlClient send back everything it received
func handlClient(conn *kcp.PeerSession) {
	buf := make([]byte, 4096)
	for {
		data := time.Now().String()

		n, err := conn.Write([]byte("AAAAAAAAA" + data))
		fmt.Println("Write:", conn.LocalAddr(), conn.RemoteAddr(), data)
		if err != nil {
			log.Println(err)
			return
		}
		n, err = conn.Read(buf)
		if err != nil {
			log.Println(err)
			return
		}
		fmt.Println("REad:", conn.LocalAddr(), conn.RemoteAddr(), buf[:n])
		time.Sleep(1 * time.Second)
	}
}

// handleEcho send back everything it received
func handleEcho(conn *kcp.PeerSession) {
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			log.Println(err)
			return
		}

		n, err = conn.Write(buf[:n])
		fmt.Println("echo:", conn.LocalAddr(), conn.RemoteAddr(), string(buf[:n]))
		if err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)
	}
}
