package main

import (
	"fmt"
	"net"
)

type UdpSvr struct {
	Addr    string
	conn    *net.UDPConn
	bufsize int
}

func NewUdpSvr(addr string, bufsize int) *UdpSvr {
	u := &UdpSvr{Addr: addr, bufsize: bufsize}
	return u
}
func (u *UdpSvr) Run(data chan<- *TranMsg) error {
	err := u.listen()
	if err != nil {
		return err
	}
	go u.read(data)
	return nil
}

func (u *UdpSvr) listen() error {
	udpAddr1, err := net.ResolveUDPAddr("udp", u.Addr)
	if err != nil {
		fmt.Println("fail to resove udp address ", udpAddr1)
		return err
	}
	conn, err := net.ListenUDP("udp4", udpAddr1)
	fmt.Println("UpdSvr Listen:", udpAddr1)
	u.conn = conn
	return err
}

func (u *UdpSvr) read(data chan<- *TranMsg) {
	for {
		datOut := make([]byte, u.bufsize)
		n, remoteAddr, err := u.conn.ReadFromUDP(datOut)
		if err == nil {

			if n > 0 {
				readString := string(datOut[:n])
				t := &TranMsg{}
				t.ClientTranport = remoteAddr.String()
				t.PubTransPath = u.Addr + "<-" + remoteAddr.String()
				t.TransId = readString
				t.from = u

				data <- t
			}
		}
	}
}

func (u *UdpSvr) Notify(data, addr string) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {

	} else {
		u.conn.WriteTo([]byte(data), udpAddr)
	}

}
