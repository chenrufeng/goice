package main

import (
	"fmt"
	"log"
	"time"

	"github.com/nkbai/goice/pnet/kcp"
)

func main() {
	listener, _ := kcp.NewListenPeer(nil, 10, 3)
	d, _ := listener.DialWithOptions("127.0.0.1:14567")
	handlClient(d)
}

// handlClient send back everything it received
func handlClient(conn *kcp.PeerSession) {
	buf := make([]byte, 4096)
	for {
		data := "AAAAAAAAA"

		n, err := conn.Write([]byte(data))
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
