package main

import (
	"net"
	"time"

	"fmt"

	"github.com/nkbai/goice/ice"
	"github.com/nkbai/log"
)

const (
	typHost = 1
	typStun = 2
	// typTurn = 3
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
func setupIcePair(typ int) (s *ice.StreamTransport, err error) {
	var cfg *ice.TransportConfig
	switch typ {
	case typHost:
		cfg = ice.NewTransportConfigHostonly()
	case typStun:
		cfg = ice.NewTransportConfigWithStun("39.108.81.146:4777")
	// case typTurn:
	// 	cfg = ice.NewTransportConfigWithTurn("39.108.81.146:4777", "bai", "bai")
	}
	s, err = ice.NewIceStreamTransport(cfg, "s")
	if err != nil {
		return
	}

	log.Trace("-----------------------------------------")
	return
}
func main() {
	s1, err := setupIcePair(typStun)
	if err != nil {
		log.Crit(err.Error())
		return
	}
	cb1 := newicecb("s1")
	s1.SetCallBack(cb1)
	err = s1.InitIce(ice.SessionRoleControlling)
	if err != nil {
		log.Crit(err.Error())
		return
	}
	// controlledRole sends out rsdp
	// rsdp, err := s2.EncodeSession()
	// if err != nil {
	// 	log.Crit(err.Error())
	// 	return
	// }
	lsdp, err := s1.EncodeSession()
	rsdp := recvrdp(lsdp)

	// UDP GET rsdp
	err = s1.StartNegotiation(rsdp)
	if err != nil {
		log.Crit(err.Error())
		return
	}

	select {
	case <-time.After(10 * time.Second):
		log.Error("s1 negotiation timeout")
		return
	case err = <-cb1.iceresult:
		if err != nil {
			log.Error(fmt.Sprintf("s1 negotiation failed %s", err))
			return
		}
		log.Error(fmt.Sprintf("s1 negotiation OK %s", err))
	}

	for {
		select {
		case <-time.After(10 * time.Second):
			s1data := []byte("hello,s2")
			fmt.Println("send:", string(s1data))
			err = s1.SendData(s1data)
			if err != nil {
				log.Crit(err.Error())
				return
			}

		case data := <-cb1.data:
			fmt.Println(string(data))
		}

	}
	log.Info("ice complete...")
}

func recvrdp(lsdp string) string {
	//connect server
	// conn, err := net.Dial("udp", "39.108.81.146:4545")
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   net.IPv4(39, 108, 81, 146),
		Port: 4545,
	})

	if err != nil {
		fmt.Printf("connect failed, err: %v\n", err)
		return ""
	}

	//send data
	fmt.Println("send:", lsdp)
	_, err = conn.Write([]byte(lsdp))
	if err != nil {
		fmt.Printf("send data failed, err : %v\n", err)
		return ""
	}

	//receive data from server
	result := make([]byte, 4096)
	go func() {
		for i := 0; i < 10; i++ {
			fmt.Println("send:", lsdp)
			_, err = conn.Write([]byte(lsdp))
			time.Sleep(time.Second * 10)
		}

	}()
	for {
		conn.SetReadDeadline(time.Now().Add(time.Second * 3))
		n, err := conn.Read(result)

		if err != nil {
			fmt.Printf("receive data failed, err: %v\n", err)

		}
		if n > 0 {
			fmt.Printf("receive from addr:  data: %v\n", string(result[:n]))
			return string(result[:n])
		}
	}

}
