package main

import (
	"fmt"
	"io"
	"log"
	"time"
)

func main() {
	client()
}

func client() {

	// wait for server to become ready
	time.Sleep(time.Second)

	// dial to the echo server
	if sess, err := DialWithOptions("127.0.0.1:12346", 10, 3); err == nil {

		for {
			data := time.Now().String()
			buf := make([]byte, len(data))
			fmt.Println("addr:", sess.LocalAddr())
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
