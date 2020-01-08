package peerkcp

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nkbai/goice/pnet/peerkcp/kcp"

	"github.com/pkg/errors"
	"golang.org/x/net/ipv4"
)

const (
	// 16-bytes nonce for each packet
	nonceSize = 16

	// 4-bytes packet checksum
	crcSize = 4

	// overall crypto header size
	cryptHeaderSize = nonceSize + crcSize

	// maximum packet size
	mtuLimit = 1500

	// FEC keeps rxFECMulti* (dataShard+parityShard) ordered packets in memory
	rxFECMulti = 3

	// accept backlog
	acceptBacklog = 128
)

var (
	errInvalidOperation = errors.New("invalid operation")
	errTimeout          = errors.New("timeout")
)

type BlockCrypt interface {
	Decrypt([]byte, []byte)
}

const fecHeaderSizePlus2 int = 0

type fecDecoder interface {
}

type fecEncoder interface {
	Encode([]byte)
}

type Entropy interface {
}

type (
	// PeerSession defines a KCP session implemented by UDP
	PeerSession struct {
		conn  net.PacketConn // the underlying packet connection
		kcp   *kcp.KCP       // KCP ARQ protocol
		l     *ListenPeer    // pointing to the ListenPeer object if it's been accepted by a ListenPeer
		block BlockCrypt     // block encryption object

		// kcp receiving is based on packets
		// recvbuf turns packets into stream
		recvbuf []byte
		bufptr  []byte

		// FEC codec
		fecDecoder *fecDecoder
		fecEncoder *fecEncoder

		// settings
		remote     net.Addr  // remote peer address
		rd         time.Time // read deadline
		wd         time.Time // write deadline
		headerSize int       // the header size additional to a KCP frame
		ackNoDelay bool      // send ack immediately for each incoming packet(testing purpose)
		writeDelay bool      // delay kcp.flush() for Write() for bulk transfer
		dup        int       // duplicate udp packets(testing purpose)

		// notifications
		die          chan struct{} // notify current session has Closed
		dieOnce      sync.Once
		chReadEvent  chan struct{} // notify Read() can be called without blocking
		chWriteEvent chan struct{} // notify Write() can be called without blocking

		// socket error handling
		socketReadError      atomic.Value
		socketWriteError     atomic.Value
		chSocketReadError    chan struct{}
		chSocketWriteError   chan struct{}
		socketReadErrorOnce  sync.Once
		socketWriteErrorOnce sync.Once

		// nonce generator
		nonce Entropy

		// packets waiting to be sent on wire
		txqueue []ipv4.Message
		// xconn           batchConn // for x/net
		xconnWriteError error

		mu sync.Mutex
	}

	setReadBuffer interface {
		SetReadBuffer(bytes int) error
	}

	setWriteBuffer interface {
		SetWriteBuffer(bytes int) error
	}

	setDSCP interface {
		SetDSCP(int) error
	}
)

// ListenPeer defines a server which will be waiting to accept incoming connections
type ListenPeer struct {
	block        BlockCrypt     // block encryption
	dataShards   int            // FEC data shard
	parityShards int            // FEC parity shard
	fecDecoder   *fecDecoder    // FEC mock initialization
	conn         net.PacketConn // the underlying packet connection

	sessions        map[string]*PeerSession // all sessions accepted by this ListenPeer
	sessionLock     sync.Mutex
	chAccepts       chan *PeerSession // Listen() backlog
	chSessionClosed chan net.Addr     // session close queue
	headerSize      int               // the additional header to a KCP frame

	die     chan struct{} // notify the listener has closed
	dieOnce sync.Once

	// socket error handling
	socketReadError     atomic.Value
	chSocketReadError   chan struct{}
	socketReadErrorOnce sync.Once

	rd atomic.Value // read deadline for Accept()
}

// 创建ListenPeer
func NewListenPeer(block BlockCrypt, dataShards, parityShards int) (*ListenPeer, error) {
	l := new(ListenPeer)
	l.conn = nil
	l.sessions = make(map[string]*PeerSession)
	l.chAccepts = make(chan *PeerSession, acceptBacklog)
	l.chSessionClosed = make(chan net.Addr)
	l.die = make(chan struct{})
	l.dataShards = dataShards
	l.parityShards = parityShards
	l.block = block
	// l.fecDecoder = newFECDecoder(rxFECMulti*(dataShards+parityShards), dataShards, parityShards)
	l.chSocketReadError = make(chan struct{})
	// calculate header size
	if l.block != nil {
		// l.headerSize += cryptHeaderSize
	}
	if l.fecDecoder != nil {
		// l.headerSize += fecHeaderSizePlus2
	}

	return l, nil
}

// 监听网络
func (l *ListenPeer) ListenWithOptions(laddr string) (*ListenPeer, error) {
	err := l.listenNetwork("udp4", laddr)
	return l, err
}

// 监听网络
func (l *ListenPeer) listenNetwork(network, laddr string) error {
	if l.conn == nil {
		var udpaddr *net.UDPAddr = nil
		var err error = nil
		if len(laddr) > 0 {
			udpaddr, err = net.ResolveUDPAddr(network, laddr)
			if err != nil {
				return errors.WithStack(err)
			}
		}

		conn, err := net.ListenUDP(network, udpaddr)
		if err != nil {
			return errors.WithStack(err)
		}

		l.conn = conn
		// 共享的读数据协程
		go l.defaultMonitor()
	}
	return nil
}

// 请求连接远端
func (l *ListenPeer) DialWithOptions(raddr string) (*PeerSession, error) {
	// network type detection
	udpaddr, err := net.ResolveUDPAddr("udp", raddr)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	err = l.listenNetwork("udp4", "")
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// 创建PeerSession
	var convid uint32
	binary.Read(rand.Reader, binary.LittleEndian, &convid)
	l.sessionLock.Lock()
	sess, ok := l.sessions[raddr]

	if ok {
		l.sessionLock.Unlock()
		return sess, nil
	}
	convid = 1024
	sess = newPeerSession(convid, l.dataShards, l.parityShards, nil, l.conn, udpaddr, l.block)
	l.sessions[raddr] = sess
	l.sessionLock.Unlock()
	return sess, nil
}

// 通知读取错误
func (l *ListenPeer) notifyReadError(err error) {
	l.socketReadErrorOnce.Do(func() {
		l.socketReadError.Store(err)
		close(l.chSocketReadError)

		// propagate read error to all sessions
		l.sessionLock.Lock()
		for _, s := range l.sessions {
			s.notifyReadError(err)
		}
		l.sessionLock.Unlock()
	})
}

// 删除一个Session
// closeSession notify the listener that a session has closed
func (l *ListenPeer) closeSession(remote net.Addr) (ret bool) {
	l.sessionLock.Lock()
	defer l.sessionLock.Unlock()
	if _, ok := l.sessions[remote.String()]; ok {
		delete(l.sessions, remote.String())
		return true
	}
	return false
}

// 接收一个Session
// AcceptKCP accepts a KCP connection
func (l *ListenPeer) AcceptKCP() (*PeerSession, error) {
	var timeout <-chan time.Time
	if tdeadline, ok := l.rd.Load().(time.Time); ok && !tdeadline.IsZero() {
		timeout = time.After(tdeadline.Sub(time.Now()))
	}

	select {
	case <-timeout:
		return nil, errors.WithStack(errTimeout)
	case c := <-l.chAccepts:
		return c, nil
	case <-l.chSocketReadError:
		return nil, l.socketReadError.Load().(error)
	case <-l.die:
		return nil, errors.WithStack(io.ErrClosedPipe)
	}
}

// 收到UDP数据包
// packet input stage
func (l *ListenPeer) packetInput(data []byte, addr net.Addr) {
	dataValid := false
	if l.block != nil {
		l.block.Decrypt(data, data)
		data = data[nonceSize:]
		checksum := crc32.ChecksumIEEE(data[crcSize:])
		if checksum == binary.LittleEndian.Uint32(data) {
			data = data[crcSize:]
			dataValid = true
		} else {
			atomic.AddUint64(&kcp.DefaultSnmp.InCsumErrors, 1)
		}
	} else if l.block == nil {
		dataValid = true
	}

	if dataValid {
		l.sessionLock.Lock()
		s, ok := l.sessions[addr.String()]
		l.sessionLock.Unlock()

		var conv, sn uint32
		convValid := false
		// if l.fecDecoder != nil {
		// 	isfec := binary.LittleEndian.Uint16(data[4:])
		// 	if isfec == typeData {
		// 		conv = binary.LittleEndian.Uint32(data[fecHeaderSizePlus2:])
		// 		sn = binary.LittleEndian.Uint32(data[fecHeaderSizePlus2+IKCP_SN_OFFSET:])
		// 		convValid = true
		// 	}
		// } else {
		conv = binary.LittleEndian.Uint32(data)
		sn = binary.LittleEndian.Uint32(data[kcp.IKCP_SN_OFFSET:])
		convValid = true
		// }

		if ok { // existing connection
			if !convValid || conv == s.kcp.GetConv() { // parity or valid data shard
				s.kcpInput(data)
			} else if sn == 0 { // should replace current connection
				// 如果存在session, 还收到包序号为0， 则关闭并重建session
				s.Close()
				s = nil
			}
		}

		// 不存在的SESSIOIN,就添加新sessioin
		if s == nil && convValid { // new session
			if len(l.chAccepts) < cap(l.chAccepts) { // do not let the new sessions overwhelm accept queue
				s := newPeerSession(conv, l.dataShards, l.parityShards, l, l.conn, addr, l.block)
				s.kcpInput(data)
				l.sessionLock.Lock()
				l.sessions[addr.String()] = s
				l.sessionLock.Unlock()
				l.chAccepts <- s
			}
		}
	}
}

// 默认共享读取数据协程
func (l *ListenPeer) defaultMonitor() {
	buf := make([]byte, mtuLimit)
	for {
		if n, from, err := l.conn.ReadFrom(buf); err == nil {
			fmt.Println("READ:::::", from.String(), n, buf[:n])
			if n >= l.headerSize+kcp.IKCP_OVERHEAD {
				l.packetInput(buf[:n], from)
			} else {
				// 小包计为错误包
				atomic.AddUint64(&kcp.DefaultSnmp.InErrs, 1)
			}
		} else {
			// 退出， 通知读错误
			l.notifyReadError(errors.WithStack(err))
			return
		}
	}
}

// 关闭整个监听端口
// Close stops listening on the UDP address, and closes the socket
func (l *ListenPeer) Close() error {
	var once bool
	l.dieOnce.Do(func() {
		close(l.die)
		once = true
	})

	if once {
		return l.conn.Close()
	} else {
		return errors.WithStack(io.ErrClosedPipe)
	}
}

// 读取数据后，KCP处理
func (s *PeerSession) kcpInput(data []byte) {
	var kcpInErrors, fecErrs, fecRecovered, fecParityShards uint64

	if s.fecDecoder != nil {
		// if len(data) > fecHeaderSize { // must be larger than fec header size
		// 	f := fecPacket(data)
		// 	if f.flag() == typeData || f.flag() == typeParity { // header check
		// 		if f.flag() == typeParity {
		// 			fecParityShards++
		// 		}

		// 		// lock
		// 		s.mu.Lock()
		// 		recovers := s.fecDecoder.Decode(f)
		// 		if f.flag() == typeData {
		// 			if ret := s.kcp.Input(data[fecHeaderSizePlus2:], true, s.ackNoDelay); ret != 0 {
		// 				kcpInErrors++
		// 			}
		// 		}

		// 		for _, r := range recovers {
		// 			if len(r) >= 2 { // must be larger than 2bytes
		// 				sz := binary.LittleEndian.Uint16(r)
		// 				if int(sz) <= len(r) && sz >= 2 {
		// 					if ret := s.kcp.Input(r[2:sz], false, s.ackNoDelay); ret == 0 {
		// 						fecRecovered++
		// 					} else {
		// 						kcpInErrors++
		// 					}
		// 				} else {
		// 					fecErrs++
		// 				}
		// 			} else {
		// 				fecErrs++
		// 			}
		// 			// recycle the recovers
		// 			kcp.XmitBuf.Put(r)
		// 		}

		// 		// to notify the readers to receive the data
		// 		if n := s.kcp.PeekSize(); n > 0 {
		// 			s.notifyReadEvent()
		// 		}
		// 		// to notify the writers
		// 		waitsnd := s.kcp.WaitSnd()
		// 		if waitsnd < int(s.kcp.snd_wnd) && waitsnd < int(s.kcp.rmt_wnd) {
		// 			s.notifyWriteEvent()
		// 		}

		// 		s.uncork()
		// 		s.mu.Unlock()
		// 	} else {
		// 		atomic.AddUint64(&kcp.DefaultSnmp.InErrs, 1)
		// 	}
		// } else {
		// 	atomic.AddUint64(&kcp.DefaultSnmp.InErrs, 1)
		// }
	} else {
		// Session收到数据
		s.mu.Lock()
		if ret := s.kcp.Input(data, true, s.ackNoDelay); ret != 0 {
			kcpInErrors++
		}
		if n := s.kcp.PeekSize(); n > 0 {
			// 通知可以读
			s.notifyReadEvent()
		}
		waitsnd := s.kcp.WaitSnd()
		if waitsnd < s.kcp.MinSndRmtWndInt() {
			// 通知可以写
			s.notifyWriteEvent()
		}
		s.uncork()
		s.mu.Unlock()
	}

	// 统计数据
	atomic.AddUint64(&kcp.DefaultSnmp.InPkts, 1)
	atomic.AddUint64(&kcp.DefaultSnmp.InBytes, uint64(len(data)))
	if fecParityShards > 0 {
		atomic.AddUint64(&kcp.DefaultSnmp.FECParityShards, fecParityShards)
	}
	if kcpInErrors > 0 {
		atomic.AddUint64(&kcp.DefaultSnmp.KCPInErrors, kcpInErrors)
	}
	if fecErrs > 0 {
		atomic.AddUint64(&kcp.DefaultSnmp.FECErrs, fecErrs)
	}
	if fecRecovered > 0 {
		atomic.AddUint64(&kcp.DefaultSnmp.FECRecovered, fecRecovered)
	}

}

// 通知读取错误
func (s *PeerSession) notifyReadError(err error) {
	s.socketReadErrorOnce.Do(func() {
		s.socketReadError.Store(err)
		close(s.chSocketReadError)
	})
}

// 通知读取事件（激动阻塞读操作）
func (s *PeerSession) notifyReadEvent() {
	select {
	case s.chReadEvent <- struct{}{}:
	default:
	}
}

// 通知写事件（激活通道满时的阻塞操作）
func (s *PeerSession) notifyWriteEvent() {
	select {
	case s.chWriteEvent <- struct{}{}:
	default:
	}
}

// uncork sends data in txqueue if there is any
func (s *PeerSession) uncork() {
	if len(s.txqueue) > 0 {
		// 发送数据
		s.defaultTx(s.txqueue)
		// recycle
		// 回收buffer
		for k := range s.txqueue {
			kcp.XmitBuf.Put(s.txqueue[k].Buffers[0])
			s.txqueue[k].Buffers = nil
		}
		s.txqueue = s.txqueue[:0]
	}
	return
}

// Close closes the connection.
func (s *PeerSession) Close() error {
	var once bool
	s.dieOnce.Do(func() {
		close(s.die)
		once = true
	})

	if once {
		atomic.AddUint64(&kcp.DefaultSnmp.CurrEstab, ^uint64(0))

		// try best to send all queued messages
		s.mu.Lock()
		s.kcp.Flush(false)
		s.uncork()
		// release pending segments
		s.kcp.ReleaseTX()
		// if s.fecDecoder != nil {
		// 	s.fecDecoder.release()
		// }
		s.mu.Unlock()

		// if s.l != nil { // belongs to listener
		// 	s.l.closeSession(s.remote)
		// 	return nil
		// } else { // client socket close
		// 	return s.conn.Close()
		// }
		return nil

	} else {
		return errors.WithStack(io.ErrClosedPipe)
	}
}

// newPeerSession create a new udp session for client or server
func newPeerSession(conv uint32, dataShards, parityShards int, l *ListenPeer, conn net.PacketConn, remote net.Addr, block BlockCrypt) *PeerSession {
	sess := new(PeerSession)
	sess.die = make(chan struct{})
	// sess.nonce = new(nonceAES128)
	// sess.nonce.Init()
	sess.chReadEvent = make(chan struct{}, 1)
	sess.chWriteEvent = make(chan struct{}, 1)
	sess.chSocketReadError = make(chan struct{})
	sess.chSocketWriteError = make(chan struct{})
	sess.remote = remote
	sess.conn = conn
	sess.l = l
	sess.block = block
	sess.recvbuf = make([]byte, mtuLimit)

	// cast to writebatch conn
	if _, ok := conn.(*net.UDPConn); ok {
		addr, err := net.ResolveUDPAddr("udp", conn.LocalAddr().String())
		if err == nil {
			if addr.IP.To4() != nil {
				// sess.xconn = ipv4.NewPacketConn(conn)
			} else {
				// sess.xconn = ipv6.NewPacketConn(conn)
			}
		}
	}

	// FEC codec initialization
	// sess.fecDecoder = newFECDecoder(rxFECMulti*(dataShards+parityShards), dataShards, parityShards)
	// if sess.block != nil {
	// 	sess.fecEncoder = newFECEncoder(dataShards, parityShards, cryptHeaderSize)
	// } else {
	// 	sess.fecEncoder = newFECEncoder(dataShards, parityShards, 0)
	// }

	// calculate additional header size introduced by FEC and encryption
	if sess.block != nil {
		sess.headerSize += cryptHeaderSize
	}
	if sess.fecEncoder != nil {
		sess.headerSize += fecHeaderSizePlus2
	}

	// 放入写出wire的回调函数
	sess.kcp = kcp.NewKCP(conv, func(buf []byte, size int) {
		if size >= kcp.IKCP_OVERHEAD+sess.headerSize {
			sess.output(buf[:size])
		}
	})
	// 加密+包头
	sess.kcp.ReserveBytes(sess.headerSize)

	if sess.l == nil { // it's a client connection
		atomic.AddUint64(&kcp.DefaultSnmp.ActiveOpens, 1)
	} else {
		atomic.AddUint64(&kcp.DefaultSnmp.PassiveOpens, 1)
	}

	// start per-session updater
	// 每个session的写数据的驱动协程
	go sess.updater()

	currestab := atomic.AddUint64(&kcp.DefaultSnmp.CurrEstab, 1)
	maxconn := atomic.LoadUint64(&kcp.DefaultSnmp.MaxConn)
	if currestab > maxconn {
		atomic.CompareAndSwapUint64(&kcp.DefaultSnmp.MaxConn, maxconn, currestab)
	}

	return sess
}

// 传输数据
func (s *PeerSession) defaultTx(txqueue []ipv4.Message) {
	nbytes := 0
	npkts := 0
	for k := range txqueue {
		// 网络写入数据
		if n, err := s.conn.WriteTo(txqueue[k].Buffers[0], txqueue[k].Addr); err == nil {
			fmt.Println("WRITETO::::", txqueue[k].Addr, n)
			nbytes += n
			npkts++
		} else {
			s.notifyWriteError(errors.WithStack(err))
			break
		}
	}
	atomic.AddUint64(&kcp.DefaultSnmp.OutPkts, uint64(npkts))
	atomic.AddUint64(&kcp.DefaultSnmp.OutBytes, uint64(nbytes))
}

// post-processing for sending a packet from kcp core
// steps:
// 1. FEC packet generation
// 2. CRC32 integrity
// 3. Encryption
// 4. TxQueue
func (s *PeerSession) output(buf []byte) {
	var ecc [][]byte

	// 1. FEC encoding
	// if s.fecEncoder != nil {
	// 	ecc = s.fecEncoder.Encode(buf)
	// }

	// 2&3. crc32 & encryption
	// if s.block != nil {
	// if s.block != nil {
	// 	s.nonce.Fill(buf[:nonceSize])
	// 	checksum := crc32.ChecksumIEEE(buf[cryptHeaderSize:])
	// 	binary.LittleEndian.PutUint32(buf[nonceSize:], checksum)
	// 	s.block.Encrypt(buf, buf)

	// 	for k := range ecc {
	// 		s.nonce.Fill(ecc[k][:nonceSize])
	// 		checksum := crc32.ChecksumIEEE(ecc[k][cryptHeaderSize:])
	// 		binary.LittleEndian.PutUint32(ecc[k][nonceSize:], checksum)
	// 		s.block.Encrypt(ecc[k], ecc[k])
	// 	}
	// }

	// 4. TxQueue
	var msg ipv4.Message
	for i := 0; i < s.dup+1; i++ {
		bts := kcp.XmitBuf.Get().([]byte)[:len(buf)]
		copy(bts, buf)
		msg.Buffers = [][]byte{bts}
		msg.Addr = s.remote
		s.txqueue = append(s.txqueue, msg)
	}

	for k := range ecc {
		bts := kcp.XmitBuf.Get().([]byte)[:len(ecc[k])]
		copy(bts, ecc[k])
		msg.Buffers = [][]byte{bts}
		msg.Addr = s.remote
		s.txqueue = append(s.txqueue, msg)
	}
}

// sess updater to trigger protocol
func (s *PeerSession) updater() {
	timer := time.NewTimer(0)
	for {
		select {
		case <-timer.C:
			s.mu.Lock()
			// 驱动KCP封装发送包
			interval := time.Duration(s.kcp.Flush(false)) * time.Millisecond
			waitsnd := uint32(s.kcp.WaitSnd())
			if waitsnd < s.kcp.MinSndRmtWnd() {
				s.notifyWriteEvent()
			}
			// 得到发送数据，并进行传输
			s.uncork()
			s.mu.Unlock()
			timer.Reset(interval)
		case <-s.die:
			timer.Stop()
			return
		}
	}
}

// 通知写错误
func (s *PeerSession) notifyWriteError(err error) {
	s.socketWriteErrorOnce.Do(func() {
		s.socketWriteError.Store(err)
		close(s.chSocketWriteError)
	})
}

// Read implements net.Conn
func (s *PeerSession) Read(b []byte) (n int, err error) {
	for {
		s.mu.Lock()
		if len(s.bufptr) > 0 { // copy from buffer into b
			n = copy(b, s.bufptr)
			s.bufptr = s.bufptr[n:]
			s.mu.Unlock()
			atomic.AddUint64(&kcp.DefaultSnmp.BytesReceived, uint64(n))
			return n, nil
		}

		if size := s.kcp.PeekSize(); size > 0 { // peek data size from kcp
			if len(b) >= size { // receive data into 'b' directly
				s.kcp.Recv(b)
				s.mu.Unlock()
				atomic.AddUint64(&kcp.DefaultSnmp.BytesReceived, uint64(size))
				return size, nil
			}

			// if necessary resize the stream buffer to guarantee a sufficent buffer space
			if cap(s.recvbuf) < size {
				s.recvbuf = make([]byte, size)
			}

			// resize the length of recvbuf to correspond to data size
			s.recvbuf = s.recvbuf[:size]
			s.kcp.Recv(s.recvbuf)
			n = copy(b, s.recvbuf)   // copy to 'b'
			s.bufptr = s.recvbuf[n:] // pointer update
			s.mu.Unlock()
			atomic.AddUint64(&kcp.DefaultSnmp.BytesReceived, uint64(n))
			return n, nil
		}

		// deadline for current reading operation
		var timeout *time.Timer
		var c <-chan time.Time
		if !s.rd.IsZero() {
			if time.Now().After(s.rd) {
				s.mu.Unlock()
				return 0, errors.WithStack(errTimeout)
			}

			delay := s.rd.Sub(time.Now())
			timeout = time.NewTimer(delay)
			c = timeout.C
		}
		s.mu.Unlock()

		// wait for read event or timeout or error
		select {
		case <-s.chReadEvent:
			if timeout != nil {
				timeout.Stop()
			}
		case <-c:
			return 0, errors.WithStack(errTimeout)
		case <-s.chSocketReadError:
			return 0, s.socketReadError.Load().(error)
		case <-s.die:
			return 0, errors.WithStack(io.ErrClosedPipe)
		}
	}
}

// Write implements net.Conn
func (s *PeerSession) Write(b []byte) (n int, err error) { return s.WriteBuffers([][]byte{b}) }

// WriteBuffers write a vector of byte slices to the underlying connection
// 发送到KCP的sn_queue中
func (s *PeerSession) WriteBuffers(v [][]byte) (n int, err error) {
	mss := s.kcp.GetMSS()
	for {
		select {
		case <-s.chSocketWriteError:
			return 0, s.socketWriteError.Load().(error)
		case <-s.die:
			return 0, errors.WithStack(io.ErrClosedPipe)
		default:
		}

		s.mu.Lock()

		// make sure write do not overflow the max sliding window on both side
		waitsnd := s.kcp.WaitSnd()
		if waitsnd < s.kcp.MinSndRmtWndInt() {
			for _, b := range v {
				n += len(b)
				for {
					if len(b) <= int(mss) {
						s.kcp.Send(b)
						break
					} else {
						s.kcp.Send(b[:mss])
						b = b[mss:]
					}
				}
			}

			waitsnd = s.kcp.WaitSnd()
			if waitsnd >= s.kcp.MinSndRmtWndInt() || !s.writeDelay {
				s.kcp.Flush(false)
				s.uncork()
			}
			s.mu.Unlock()
			atomic.AddUint64(&kcp.DefaultSnmp.BytesSent, uint64(n))
			return n, nil
		}

		var timeout *time.Timer
		var c <-chan time.Time
		if !s.wd.IsZero() {
			if time.Now().After(s.wd) {
				s.mu.Unlock()
				return 0, errors.WithStack(errTimeout)
			}
			delay := s.wd.Sub(time.Now())
			timeout = time.NewTimer(delay)
			c = timeout.C
		}
		s.mu.Unlock()

		select {
		case <-s.chWriteEvent:
			if timeout != nil {
				timeout.Stop()
			}
		case <-c:
			return 0, errors.WithStack(errTimeout)
		case <-s.chSocketWriteError:
			return 0, s.socketWriteError.Load().(error)
		case <-s.die:
			return 0, errors.WithStack(io.ErrClosedPipe)
		}
	}
}

// LocalAddr returns the local network address. The Addr returned is shared by all invocations of LocalAddr, so do not modify it.
func (s *PeerSession) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// RemoteAddr returns the remote network address. The Addr returned is shared by all invocations of RemoteAddr, so do not modify it.
func (s *PeerSession) RemoteAddr() net.Addr { return s.remote }

// SetDeadline sets the deadline associated with the listener. A zero time value disables the deadline.
func (s *PeerSession) SetDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rd = t
	s.wd = t
	s.notifyReadEvent()
	s.notifyWriteEvent()
	return nil
}

// SetReadDeadline implements the Conn SetReadDeadline method.
func (s *PeerSession) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rd = t
	s.notifyReadEvent()
	return nil
}

// SetWriteDeadline implements the Conn SetWriteDeadline method.
func (s *PeerSession) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wd = t
	s.notifyWriteEvent()
	return nil
}
