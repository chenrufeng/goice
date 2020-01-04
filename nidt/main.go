package main

import (
	"flag"
	"fmt"

	"github.com/nkbai/goice/nidt/settings"
)

// NAT识别服务器
var fileSetting = flag.String("setting", "config.json", "file path name of settings")

func main() {
	err := settings.Init(*fileSetting)
	if err != nil {
		fmt.Println("fail to read config file ", *fileSetting, err.Error())
		return
	}

	// 创建NAT类型判断器
	n := NewNat(settings.GetExpire())

	// 创建服务器
	u1 := NewUdpSvr(settings.GetAddr1(), settings.GetBufferSize())
	u2 := NewUdpSvr(settings.GetAddr2(), settings.GetBufferSize())
	u1.Run(n.netrecv)
	u2.Run(n.netrecv)

	// 侧路通知器
	o := NewOutputSvr(settings.GetAddr3())
	o.Run()
	// 取得判断器结论
	// 侧路和服务器通知返回
	n.notifers = append(n.notifers, u1)
	n.notifers = append(n.notifers, u2)
	n.SideNotifers = append(n.SideNotifers, o)
	n.Run()

}
