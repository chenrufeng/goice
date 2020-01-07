package main

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"

	"sync/atomic"
	"time"

	"github.com/pkg/errors"

	"github.com/nkbai/goice/pnet/kcp"

	"golang.org/x/net/ipv4"
)

func init() {
	xmitBuf.New = func() interface{} {
		return make([]byte, mtuLimit)
	}
}


func ListenWithOptions(laddr string /*block BlockCrypt,*/, dataShards, parityShards int) (*ListenPeer, error) {
	udpaddr, err := net.ResolveUDPAddr("udp", laddr)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	conn, err := net.ListenUDP("udp", udpaddr)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return ServeConn( /*block,*/ dataShards, parityShards, conn)
}

// ServeConn serves KCP protocol for a single packet connection.
func ServeConn( /*block BlockCrypt,*/ dataShards, parityShards int, conn net.PacketConn) (*ListenPeer, error) {
	l := new(ListenPeer)
	l.conn = conn
	l.sessions = make(map[string]*PeerSession)
	l.chAccepts = make(chan *PeerSession, acceptBacklog)
	l.chSessionClosed = make(chan net.Addr)
	l.die = make(chan struct{})
	l.dataShards = dataShards
	l.parityShards = parityShards
	l.chSocketReadError = make(chan struct{})

	go l.defaultMonitor()
	return l, nil
}

func (s *PeerSession) defaultReadLoop() {
	buf := make([]byte, mtuLimit)
	var src string
	for {
		if n, addr, err := s.conn.ReadFrom(buf); err == nil {
			// make sure the packet is from the same source
			if src == "" { // set source address
				src = addr.String()
			} else if addr.String() != src {
				atomic.AddUint64(&kcp.DefaultSnmp.InErrs, 1)
				continue
			}

			if n >= s.headerSize+kcp.IKCP_OVERHEAD {
				s.packetInput(buf[:n])
			} else {
				atomic.AddUint64(&kcp.DefaultSnmp.InErrs, 1)
			}
		} else {
			s.notifyReadError(errors.WithStack(err))
			return
		}
	}
}

func (l *ListenPeer) defaultMonitor() {
	buf := make([]byte, mtuLimit)
	for {
		if n, from, err := l.conn.ReadFrom(buf); err == nil {
			if n >= l.headerSize+kcp.IKCP_OVERHEAD {
				l.packetInput(buf[:n], from)
			} else {
				atomic.AddUint64(&kcp.DefaultSnmp.InErrs, 1)
			}
		} else {
			l.notifyReadError(errors.WithStack(err))
			return
		}
	}
}

// packet input stage
func (s *PeerSession) packetInput(data []byte) {
	dataValid := false
	dataValid = true


	if dataValid {
		s.kcpInput(data)
	}
}

func (s *PeerSession) kcpInput(data []byte) {
	var kcpInErrors, fecErrs, fecRecovered, fecParityShards uint64

	s.mu.Lock()
	if ret := s.kcp.Input(data, true, s.ackNoDelay); ret != 0 {
		kcpInErrors++
	}
	if n := s.kcp.PeekSize(); n > 0 {
		s.notifyReadEvent()
	}
	waitsnd := uint32(s.kcp.WaitSnd())
	if waitsnd < s.kcp.MinSndRmtWnd() {
		s.notifyWriteEvent()
	}
	s.uncork()
	s.mu.Unlock()


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

func (s *PeerSession) notifyReadError(err error) {
	s.socketReadErrorOnce.Do(func() {
		s.socketReadError.Store(err)
		close(s.chSocketReadError)
	})
}

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

func (s *PeerSession) notifyReadEvent() {
	select {
	case s.chReadEvent <- struct{}{}:
	default:
	}
}

func (s *PeerSession) notifyWriteEvent() {
	select {
	case s.chWriteEvent <- struct{}{}:
	default:
	}
}

// packet input stage
func (l *ListenPeer) packetInput(data []byte, addr net.Addr) {
	dataValid := false
	dataValid = true


	if dataValid {
		l.sessionLock.Lock()
		s, ok := l.sessions[addr.String()]
		l.sessionLock.Unlock()

		var conv, sn uint32
		convValid := false

		conv = binary.LittleEndian.Uint32(data)
		sn = binary.LittleEndian.Uint32(data[kcp.IKCP_SN_OFFSET:])
		convValid = true
		

		if ok { // existing connection
			if !convValid || conv == s.kcp.GetConv() { // parity or valid data shard
				s.kcpInput(data)
			} else if sn == 0 { // should replace current connection
				s.Close()
				s = nil
			}
		}

		if s == nil && convValid { // new session
			if len(l.chAccepts) < cap(l.chAccepts) { // do not let the new sessions overwhelm accept queue
				s := newPeerSession(conv, l.dataShards, l.parityShards, l, l.conn, addr /*, l.block*/)
				s.kcpInput(data)
				l.sessionLock.Lock()
				l.sessions[addr.String()] = s
				l.sessionLock.Unlock()
				l.chAccepts <- s
			}
		}
	}
}

// uncork sends data in txqueue if there is any
func (s *PeerSession) uncork() {
	if len(s.txqueue) > 0 {
		s.tx(s.txqueue)
		// recycle
		for k := range s.txqueue {
			xmitBuf.Put(s.txqueue[k].Buffers[0])
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

		if s.l != nil { // belongs to listener
			s.l.closeSession(s.remote)
			return nil
		} else { // client socket close
			return s.conn.Close()
		}
	} else {
		return errors.WithStack(io.ErrClosedPipe)
	}
}

// newPeerSession create a new udp session for client or server
func newPeerSession(conv uint32, dataShards, parityShards int, l *ListenPeer, conn net.PacketConn, remote net.Addr /*, block BlockCrypt*/) *PeerSession {
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
	// sess.block = block
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


	sess.kcp = kcp.NewKCP(conv, func(buf []byte, size int) {
		if size >= kcp.IKCP_OVERHEAD+sess.headerSize {
			sess.output(buf[:size])
		}
	})
	sess.kcp.ReserveBytes(sess.headerSize)

	if sess.l == nil { // it's a client connection
		go sess.readLoop()
		atomic.AddUint64(&kcp.DefaultSnmp.ActiveOpens, 1)
	} else {
		atomic.AddUint64(&kcp.DefaultSnmp.PassiveOpens, 1)
	}

	// start per-session updater
	go sess.updater()

	currestab := atomic.AddUint64(&kcp.DefaultSnmp.CurrEstab, 1)
	maxconn := atomic.LoadUint64(&kcp.DefaultSnmp.MaxConn)
	if currestab > maxconn {
		atomic.CompareAndSwapUint64(&kcp.DefaultSnmp.MaxConn, maxconn, currestab)
	}

	return sess
}

func (s *PeerSession) tx(txqueue []ipv4.Message) {
	s.defaultTx(txqueue)
}

func (s *PeerSession) defaultTx(txqueue []ipv4.Message) {
	nbytes := 0
	npkts := 0
	for k := range txqueue {
		if n, err := s.conn.WriteTo(txqueue[k].Buffers[0], txqueue[k].Addr); err == nil {
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

// post-processing for sending a packet from kcp core
// steps:
// 1. FEC packet generation
// 2. CRC32 integrity
// 3. Encryption
// 4. TxQueue
func (s *PeerSession) output(buf []byte) {
	var ecc [][]byte

	// 4. TxQueue
	var msg ipv4.Message
	for i := 0; i < s.dup+1; i++ {
		bts := xmitBuf.Get().([]byte)[:len(buf)]
		copy(bts, buf)
		msg.Buffers = [][]byte{bts}
		msg.Addr = s.remote
		s.txqueue = append(s.txqueue, msg)
	}

	for k := range ecc {
		bts := xmitBuf.Get().([]byte)[:len(ecc[k])]
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
			interval := time.Duration(s.kcp.Flush(false)) * time.Millisecond
			waitsnd := uint32(s.kcp.WaitSnd())
			if waitsnd < s.kcp.MinSndRmtWnd() {
				s.notifyWriteEvent()
			}
			s.uncork()
			s.mu.Unlock()
			timer.Reset(interval)
		case <-s.die:
			timer.Stop()
			return
		}
	}
}

func (s *PeerSession) readLoop() {
	s.defaultReadLoop()
}

func (s *PeerSession) notifyWriteError(err error) {
	s.socketWriteErrorOnce.Do(func() {
		s.socketWriteError.Store(err)
		close(s.chSocketWriteError)
	})
}

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

// Dial connects to the remote address "raddr" on the network "udp" without encryption and FEC
func Dial(raddr string) (net.Conn, error) { return DialWithOptions(raddr /*nil,*/, 0, 0) }

// DialWithOptions connects to the remote address "raddr" on the network "udp" with packet encryption
//
// 'block' is the block encryption algorithm to encrypt packets.
//
// 'dataShards', 'parityShards' specifiy how many parity packets will be generated following the data packets.
//
// Check https://github.com/klauspost/reedsolomon for details
func DialWithOptions(raddr string /*block BlockCrypt,*/, dataShards, parityShards int) (*PeerSession, error) {
	// network type detection
	udpaddr, err := net.ResolveUDPAddr("udp", raddr)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	network := "udp4"
	if udpaddr.IP.To4() == nil {
		network = "udp"
	}

	conn, err := net.ListenUDP(network, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return NewConn(raddr /* block,*/, dataShards, parityShards, conn)
}


// NewConn establishes a session and talks KCP protocol over a packet connection.
func NewConn(raddr string /*block BlockCrypt,*/, dataShards, parityShards int, conn net.PacketConn) (*PeerSession, error) {
	udpaddr, err := net.ResolveUDPAddr("udp", raddr)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var convid uint32
	binary.Read(rand.Reader, binary.LittleEndian, &convid)	
	return newPeerSession(convid, dataShards, parityShards, nil, conn, udpaddr /*, block*/), nil
}
