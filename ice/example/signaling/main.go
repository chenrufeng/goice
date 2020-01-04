package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"
)

type AddrTimePair struct {
	UAddr *net.UDPAddr
	Time  int64
}

var (
	UDP_EXPIRE    = flag.Int64("expire", 100, "expiration of seconds")
	BUFF_SIZE     = flag.Int("buf", 1024, "size of recving buffer")
	listenAddress = flag.String("addr", "0.0.0.0:4545", "udp endpoint to listen")
)

func main() {
	flag.Parse()
	addr, err := net.ResolveUDPAddr("udp", *listenAddress)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	defer conn.Close()

	clients := make(map[string]AddrTimePair)
	for {
		// Here must use make and give the lenth of buffer
		data := make([]byte, *BUFF_SIZE)
		n, rAddr, err := conn.ReadFromUDP(data)
		if err != nil {
			fmt.Println(err)
			continue
		}
		if n >= *BUFF_SIZE {
			fmt.Println("over flow recv buffer")
			continue
		}

		clients[rAddr.String()] = AddrTimePair{rAddr, time.Now().Unix()}

		strData := string(data)
		fmt.Println("Received from:", rAddr, ":", strData)

		var errclients []string
		for k, v := range clients {
			fmt.Println("kv:", k, v)
			if k == rAddr.String() {
				continue
			}
			_, err = conn.WriteToUDP(data[:n], v.UAddr)
			if err != nil {
				fmt.Println(err)
				errclients = append(errclients, k)
				continue
			} else if time.Now().Unix()-v.Time > *UDP_EXPIRE {
				errclients = append(errclients, k)
			}

			fmt.Println("Sendto [", v.UAddr, "]:", string(data))
		}
		for k, v := range errclients {
			fmt.Println("lost kv:", k, v)
			delete(clients, v)
		}

	}
}
