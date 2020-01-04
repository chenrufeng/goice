package main

import (
	"container/heap"
	"time"
)

type TranMsg struct {
	ClientTranport string
	PubTransPath   string
	TransId        string
	from           notifer
	priority       int64
	index          int
}

// A PriorityQueue implements heap.Interface and holds Items.
type PriorityQueue []*TranMsg

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	return pq[i].priority < pq[j].priority
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *PriorityQueue) Push(x interface{}) {
	n := len(*pq)
	item := x.(*TranMsg)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // for safety
	*pq = old[0 : n-1]
	return item
}

func (pq *PriorityQueue) Peek() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	return item
}

type Nat struct {
	netrecv      chan *TranMsg
	pairData     map[string]*TranMsg // TransId Vs PeerTransPort
	pq           PriorityQueue
	notifers     []notifer
	SideNotifers []notifer
	expire       int64
}

func NewNat(expire int64) *Nat {
	n := &Nat{}
	n.netrecv = make(chan *TranMsg)
	n.pairData = make(map[string]*TranMsg)
	n.pq = make(PriorityQueue, 0)
	n.expire = expire
	return n
}

func (n *Nat) Run() {

	for {
		select {
		case <-time.After(1 * time.Second):

			n.expireMatch()
		case dat, ok := <-n.netrecv:
			if ok {
				n.handIncomingData(dat)
			} else {
				return
			}
		}
	}
}

func (n *Nat) expireMatch() {
	for n.pq.Len() > 0 {
		t := n.pq.Peek().(*TranMsg)
		if t.priority < time.Now().Unix() {
			_, ok := n.pairData[t.TransId]
			if ok {
				n.notifyBack(t.TransId+"|"+t.PubTransPath, t.ClientTranport, t.from)
				// 侧路探测
				n.sideNotify(t.TransId+"|"+t.PubTransPath+"|"+"->", t.ClientTranport)
				delete(n.pairData, t.TransId)
			} else {
				// 已经处理的数据，不需要再处理了
			}

			n.pq.Pop()
		} else {
			return
		}
	}

}
func (n *Nat) handIncomingData(t *TranMsg) {
	// 是否为第一个消息， 先缓存
	dat, ok := n.pairData[t.TransId]
	if ok {
		// 第二条信息处理返回
		n.notifyBack(dat.TransId+"|"+dat.PubTransPath+"|"+t.PubTransPath, dat.ClientTranport, dat.from)
		n.notifyBack(dat.TransId+"|"+dat.PubTransPath+"|"+t.PubTransPath, t.ClientTranport, t.from)
		// 侧路探测
		n.sideNotify(dat.TransId+"|"+dat.PubTransPath+"|"+t.PubTransPath+"|"+"->", t.ClientTranport)
		n.sideNotify(dat.TransId+"|"+dat.PubTransPath+"|"+t.PubTransPath+"|"+"->", dat.ClientTranport)
		delete(n.pairData, t.TransId)
	} else {
		// 缓存等待
		n.pairData[t.TransId] = t
		t.priority = time.Now().Unix() + n.expire
		heap.Push(&n.pq, t)
	}
}

func (n *Nat) notifyBack(msg, addr string, from notifer) {
	for _, v := range n.notifers {
		if v == from {
			v.Notify(msg, addr)
		}
	}
}

func (n *Nat) sideNotify(msg, addr string) {
	for _, v := range n.SideNotifers {

		v.Notify(msg, addr)
	}
}
