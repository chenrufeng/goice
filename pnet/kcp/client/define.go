package main

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"

	"github.com/nkbai/goice/pnet/kcp"

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

var (
	// a system-wide packet buffer shared among sending, receiving and FEC
	// to mitigate high-frequency memory allocation for packets
	xmitBuf sync.Pool
)

type (
	// PeerSession defines a KCP session implemented by UDP
	PeerSession struct {
		conn net.PacketConn // the underlying packet connection
		kcp  *kcp.KCP       // KCP ARQ protocol
		l    *ListenPeer    // pointing to the ListenPeer object if it's been accepted by a ListenPeer
		// block BlockCrypt     // block encryption object

		// kcp receiving is based on packets
		// recvbuf turns packets into stream
		recvbuf []byte
		bufptr  []byte

		// FEC codec
		// fecDecoder *fecDecoder
		// fecEncoder *fecEncoder

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
		// nonce Entropy

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
	// block        BlockCrypt     // block encryption
	dataShards   int // FEC data shard
	parityShards int // FEC parity shard
	// fecDecoder   *fecDecoder    // FEC mock initialization
	conn net.PacketConn // the underlying packet connection

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
