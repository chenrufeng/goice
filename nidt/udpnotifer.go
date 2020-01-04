package main

import (
	"fmt"
	"net"
)

type notifer interface {
	Notify(data, addr string)
}

type OutputSvr struct {
	Addr string
	conn *net.UDPConn
}

func NewOutputSvr(addr string) *OutputSvr {
	u := &OutputSvr{Addr: addr}
	return u
}
func (u *OutputSvr) Run() error {
	err := u.listen()
	if err != nil {
		return err
	}
	return nil
}

func (u *OutputSvr) listen() error {
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

func (u *OutputSvr) Notify(data, addr string) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {

	} else {
		u.conn.WriteTo([]byte(data), udpAddr)
	}

}
