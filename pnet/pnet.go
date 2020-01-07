package pnet

type Ipnet interface {
	Stop()
	Init()
	Start()

	TransJob()             // 开启一个文件传输工作// 块链表
	CancelJob()            // 取消一个文件传输工作// 块链表
	PauseJob()             // 暂停一个文件传输工作// 块链表
	RecieveBlockCallback() // 回调收到数据块

	IgoreBlocks() // 不用下载某些块

	AdjustParam() // 调整参数
}

type P2pNetMgr struct {
}

// 初始化
func (p *P2pNetMgr) Init() {

}

// 连接Tracker服务器
func (p *P2pNetMgr) ConnectTracker() {

}

// 取得相关节点
func (p *P2pNetMgr) FetchRalativePeer() {

}

// 添加下载点
func (p *P2pNetMgr) AddSeeders() {

}
