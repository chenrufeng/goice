package main

import (
	"net"
	"os"
	"strings"

	"fmt"

	"github.com/nkbai/goice/ice"
	"github.com/nkbai/log"
)

const (
	typHost = 1
	typStun = 2
	typTurn = 3
)

type icecb struct {
	data      chan []byte
	iceresult chan error
	name      string
}

func newicecb(name string) *icecb {
	return &icecb{
		name:      name,
		data:      make(chan []byte, 1),
		iceresult: make(chan error, 1),
	}
}
func (c *icecb) OnReceiveData(data []byte, from net.Addr) {
	c.data <- data
}

/*
	Callback to report status of various ICE operations.
*/
func (c *icecb) OnIceComplete(result error) {
	c.iceresult <- result
	log.Trace(fmt.Sprintf("%s negotiation complete", c.name))
}
func setupIcePair(typ int) (s1, s2 *ice.StreamTransport, err error) {
	var cfg *ice.TransportConfig
	switch typ {
	case typHost:
		cfg = ice.NewTransportConfigHostonly()
	case typStun:
		cfg = ice.NewTransportConfigWithStun("39.108.81.146:4777")
	case typTurn:
		cfg = ice.NewTransportConfigWithTurn("39.108.81.146:4777", "bai", "bai")
	}
	s1, err = ice.NewIceStreamTransport(cfg, "s1")
	if err != nil {
		return
	}
	s2, err = ice.NewIceStreamTransport(cfg, "s2")
	log.Trace("-----------------------------------------")
	return
}
func main() {
	var cfg *ice.TransportConfig
	// cfg = ice.NewTransportConfigWithTurn("39.108.81.146:4777", "bai", "bai")
	// s1, err := ice.NewTurnSock(cfg.TurnSever, cfg.TurnUserName, cfg.TurnPassword)
	cfg = ice.NewTransportConfigWithStun("39.108.81.146:4777")
	s1, err := ice.NewStunSocket(cfg.StunSever)
	candidates, err := s1.GetCandidates()
	for _, candi := range candidates {
		fmt.Println(candi.String())
	}
	fmt.Println("local Address:", s1.LocalAddr)
	s1.ReuseDial(s1.LocalAddr, "stun.l.google.com:19302")
	candidates, err = s1.GetCandidates()
	for _, candi := range candidates {
		fmt.Println(candi.String())
	}
	fmt.Println("local Address:", s1.LocalAddr)

	// s1.Close()
	addr, _ := net.ResolveUDPAddr("udp", s1.LocalAddr)
	fmt.Println("---------")

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	defer conn.Close()

	for {
		// Here must use make and give the lenth of buffer
		data := make([]byte, 1024)
		_, rAddr, err := conn.ReadFromUDP(data)
		if err != nil {
			fmt.Println(err)
			continue
		}

		strData := string(data)
		fmt.Println("Received[%v]:", rAddr, strData)

		upper := strings.ToUpper(strData)
		_, err = conn.WriteToUDP([]byte(upper), rAddr)
		if err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Println("Send:", rAddr, upper)

		peerb, _ := net.ResolveUDPAddr("udp", "183.14.29.235:11111")

		_, err = conn.WriteToUDP([]byte(upper), peerb)
		if err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Println("Send:", peerb, upper)
	}
	// fmt.Println(net.ListenUDP("udp4", addr))
	// fmt.Println("---------")
	log.Info("ice complete...", candidates, err)
}
