package raknet

import (
	"bytes"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"

	"github.com/df-mc/atomic"
	"github.com/sandertv/go-raknet/internal/message"
	"github.com/sandertv/go-raknet/query"
	"golang.org/x/exp/slices"
)

// UpstreamPacketListener allows for a custom PacketListener implementation.
type UpstreamPacketListener interface {
	ListenPacket(network, address string) (net.PacketConn, error)
}

// ListenConfig may be used to pass additional configuration to a Listener.
type ListenConfig struct {
	// ProtocolVersions represents the protocol versions to allow over the network. If empty, this will just contain
	// currentProtocol.
	ProtocolVersions []byte

	// ErrorLog is a logger that errors from packet decoding are logged to. It may be set to a logger that
	// simply discards the messages.
	ErrorLog *log.Logger

	// UpstreamPacketListener adds an abstraction for net.ListenPacket.
	UpstreamPacketListener UpstreamPacketListener

	// If enabled, respond to UT3 query requests
	EnableQuery bool
}

// Listener implements a RakNet connection listener. It follows the same methods as those implemented by the
// TCPListener in the net package.
// Listener implements the net.Listener interface.
type Listener struct {
	once   sync.Once
	closed chan struct{}

	// log is a logger that errors from packet decoding are logged to. It may be set to a logger that
	// simply discards the messages.
	log *log.Logger

	conn net.PacketConn
	// incoming is a channel of incoming connections. Connections that end up in here will also end up in
	// the connections map.
	incoming chan *Conn

	// connections is a map of currently active connections, indexed by their address.
	connections sync.Map

	// id is a random server ID generated upon starting listening. It is used several times throughout the
	// connection sequence of RakNet.
	id int64

	// protocols is a list of protocol versions that are accepted by the listener.
	protocols []byte

	// pongData is a byte slice of data that is sent in an unconnected pong packet each time the client sends
	// and unconnected ping to the server.
	pongData atomic.Value[[]byte]

	// blockedAddresses contains ip addresses that are blocked and the time they will be unblocked
	blockedAddresses sync.Map

	// numPackets contains the number of packets sent per ip in the last second
	numPackets sync.Map

	//queryHandler is the UT3 query handler
	queryHandler *query.QueryHandler

	// If enabled, respond to UT3 query requests
	enableQuery bool
}

// listenerID holds the next ID to use for a Listener.
var listenerID = atomic.NewInt64(rand.New(rand.NewSource(time.Now().Unix())).Int63())

// Listen listens on the address passed and returns a listener that may be used to accept connections. If not
// successful, an error is returned.
// The address follows the same rules as those defined in the net.TCPListen() function.
// Specific features of the listener may be modified once it is returned, such as the used log and/or the
// accepted protocol.
func (l ListenConfig) Listen(address string) (*Listener, error) {
	var conn net.PacketConn
	var err error

	if l.UpstreamPacketListener == nil {
		conn, err = net.ListenPacket("udp", address)
	} else {
		conn, err = l.UpstreamPacketListener.ListenPacket("udp", address)
	}
	if err != nil {
		return nil, &net.OpError{Op: "listen", Net: "raknet", Source: nil, Addr: nil, Err: err}
	}
	listener := &Listener{
		closed:       make(chan struct{}),
		conn:         conn,
		id:           listenerID.Inc(),
		incoming:     make(chan *Conn),
		log:          log.New(os.Stderr, "", log.LstdFlags),
		protocols:    []byte{currentProtocol},
		queryHandler: query.New(map[string]string{}, []string{}),
		enableQuery:  l.EnableQuery,
	}
	if l.ErrorLog != nil {
		listener.log = l.ErrorLog
	}
	if len(l.ProtocolVersions) > 0 {
		listener.protocols = append(listener.protocols, l.ProtocolVersions...)
	}

	go listener.listen()
	return listener, nil
}

// Listen listens on the address passed and returns a listener that may be used to accept connections. If not
// successful, an error is returned.
// The address follows the same rules as those defined in the net.TCPListen() function.
// Specific features of the listener may be modified once it is returned, such as the used log and/or the
// accepted protocol.
func Listen(address string) (*Listener, error) {
	var lc ListenConfig
	return lc.Listen(address)
}

// Accept blocks until a connection can be accepted by the listener. If successful, Accept returns a
// connection that is ready to send and receive data. If not successful, a nil listener is returned and an
// error describing the problem.
func (listener *Listener) Accept() (net.Conn, error) {
	conn, ok := <-listener.incoming
	if !ok {
		return nil, &net.OpError{Op: "accept", Net: "raknet", Source: nil, Addr: nil, Err: errListenerClosed}
	}
	return conn, nil
}

// Addr returns the address the Listener is bound to and listening for connections on.
func (listener *Listener) Addr() net.Addr {
	return listener.conn.LocalAddr()
}

// Close closes the listener so that it may be cleaned up. It makes sure the goroutine handling incoming
// packets is able to be freed.
func (listener *Listener) Close() error {
	var err error
	listener.once.Do(func() {
		close(listener.closed)
		err = listener.conn.Close()
	})
	return err
}

// PongData sets the pong data that is used to respond with when a client sends a ping. It usually holds game
// specific data that is used to display in a server list.
// If a data slice is set with a size bigger than math.MaxInt16, the function panics.
func (listener *Listener) PongData(data []byte) {
	if len(data) > math.MaxInt16 {
		panic(fmt.Sprintf("error setting pong data: pong data must not be longer than %v", math.MaxInt16))
	}
	listener.pongData.Store(data)
}

// ID returns the unique ID of the listener. This ID is usually used by a client to identify a specific
// server during a single session.
func (listener *Listener) ID() int64 {
	return listener.id
}

func (listener *Listener) BlockAddress(ip net.Addr, duration time.Duration) {
	host, _, _ := net.SplitHostPort(ip.String())
	listener.log.Printf("Blocked %s for %f seconds", ip, duration.Seconds())
	listener.blockedAddresses.Store(host, time.Now().Add(duration))
}

func (listener *Listener) UnblockAddress(ip net.Addr) {
	host, _, _ := net.SplitHostPort(ip.String())
	listener.blockedAddresses.Delete(host)
}

func (listener *Listener) SetQueryInfo(queryInfo map[string]string) {
	listener.queryHandler.SetQueryInfo(queryInfo)
}

func (listener *Listener) SetQueryPlayers(queryPlayers []string) {
	listener.queryHandler.SetPlayers(queryPlayers)
}

// listen continuously reads from the listener's UDP connection, until closed has a value in it.
func (listener *Listener) listen() {
	// Create a buffer with the maximum size a UDP packet sent over RakNet is allowed to have. We can re-use
	// this buffer for each packet.
	b := make([]byte, 1500)
	buf := bytes.NewBuffer(b[:0])
	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for {
			select {
			case <-listener.closed:
				return
			case <-ticker.C:
				listener.numPackets.Range(func(key interface{}, value interface{}) bool {
					listener.numPackets.Delete(key)
					return true
				})
				listener.blockedAddresses.Range(func(key interface{}, value interface{}) bool {
					unbanTime, ok := value.(time.Time)
					if ok && time.Now().After(unbanTime) {
						listener.blockedAddresses.Delete(key)
					}
					return true
				})
			}
		}
	}()
	for {
		n, addr, err := listener.conn.ReadFrom(b)
		if err != nil {
			close(listener.incoming)
			return
		}
		_, _ = buf.Write(b[:n])

		// Technically we should not re-use the same byte slice after its ownership has been taken by the
		// buffer, but we can do this anyway because we copy the data later.
		listener.handle(buf, addr)
		buf.Reset()
	}
}

// handle handles an incoming packet in buffer b from the address passed. If not successful, an error is
// returned describing the issue.
func (listener *Listener) handle(b *bytes.Buffer, addr net.Addr) error {
	ip, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return err
	}

	if _, ok := listener.blockedAddresses.Load(ip); ok {
		return nil
	}

	num, _ := listener.numPackets.Load(ip)
	numPackets, ok := num.(int)
	if !ok {
		numPackets = 0
	}
	listener.numPackets.Store(ip, numPackets+1)

	if numPackets > 10000 {
		listener.BlockAddress(addr, 300*time.Second)
	}

	value, found := listener.connections.Load(addr.String())
	if !found {
		// If there was no session yet, it means the packet is an offline message. It is not contained in a
		// datagram.
		packetID, err := b.ReadByte()
		if err != nil {
			listener.BlockAddress(addr, 5*time.Second)
			return fmt.Errorf("error reading packet ID byte: %v", err)
		}
		switch packetID {
		case message.IDUnconnectedPing, message.IDUnconnectedPingOpenConnections:
			return listener.handleUnconnectedPing(b, addr)
		case message.IDOpenConnectionRequest1:
			return listener.handleOpenConnectionRequest1(b, addr)
		case query.Header[0]:
			if listener.enableQuery {
				return listener.handleQuery(b, addr)
			}
		default:
			// In some cases, the client will keep trying to send datagrams while it has already timed out. In
			// this case, we should not print an error.
			if packetID&bitFlagDatagram == 0 {
				listener.BlockAddress(addr, 5*time.Second)
				return fmt.Errorf("unknown packet received (%x): %x", packetID, b.Bytes())
			}
		}
		return nil
	}
	conn := value.(*Conn)
	select {
	case <-conn.closed:
		// Connection was closed already.
		return nil
	default:
		err := conn.receive(b)
		if err != nil {
			conn.closeImmediately()
		}
		return err
	}
}

// handleOpenConnectionRequest1 handles an open connection request 1 packet stored in buffer b, coming from
// an address addr.
func (listener *Listener) handleOpenConnectionRequest1(b *bytes.Buffer, addr net.Addr) error {
	packet := &message.OpenConnectionRequest1{}
	if err := packet.Read(b); err != nil {
		return fmt.Errorf("error reading open connection request 1: %v", err)
	}
	b.Reset()

	mtuSize := packet.MaximumSizeNotDropped
	if mtuSize > maxMTUSize {
		mtuSize = maxMTUSize
	}

	if !slices.Contains(listener.protocols, packet.Protocol) {
		(&message.IncompatibleProtocolVersion{ServerGUID: listener.id, ServerProtocol: currentProtocol}).Write(b)
		_, _ = listener.conn.WriteTo(b.Bytes(), addr)
		return fmt.Errorf("error handling open connection request 1: incompatible protocol version %v (listener protocol = %v)", packet.Protocol, currentProtocol)
	}

	(&message.OpenConnectionReply1{ServerGUID: listener.id, Secure: false, ServerPreferredMTUSize: mtuSize}).Write(b)
	if _, err := listener.conn.WriteTo(b.Bytes(), addr); err != nil {
		return fmt.Errorf("error sending open connection reply 2: %v", err)
	}
	b.Reset()

	(&message.OpenConnectionReply2{ServerGUID: listener.id, ClientAddress: *addr.(*net.UDPAddr), MTUSize: mtuSize}).Write(b)
	if _, err := listener.conn.WriteTo(b.Bytes(), addr); err != nil {
		return fmt.Errorf("error sending open connection reply 2: %v", err)
	}

	conn := newConn(listener.conn, addr, packet.Protocol, mtuSize)
	conn.close = func() {
		// Make sure to remove the connection from the Listener once the Conn is closed.
		listener.connections.Delete(addr.String())
	}
	listener.connections.Store(addr.String(), conn)

	go func() {
		t := time.NewTimer(time.Second * 10)
		defer t.Stop()
		select {
		case <-conn.connected:
			// Add the connection to the incoming channel so that a caller of Accept() can receive it.
			listener.incoming <- conn
		case <-listener.closed:
			_ = conn.Close()
		case <-t.C:
			// It took too long to complete this connection. We closed it and go back to accepting.
			_ = conn.Close()
		}
	}()
	return nil
}

// handleUnconnectedPing handles an unconnected ping packet stored in buffer b, coming from an address addr.
func (listener *Listener) handleUnconnectedPing(b *bytes.Buffer, addr net.Addr) error {
	pk := &message.UnconnectedPing{}
	if err := pk.Read(b); err != nil {
		return fmt.Errorf("error reading unconnected ping: %v", err)
	}
	b.Reset()

	(&message.UnconnectedPong{ServerGUID: listener.id, SendTimestamp: pk.SendTimestamp, Data: listener.pongData.Load()}).Write(b)
	_, err := listener.conn.WriteTo(b.Bytes(), addr)
	return err
}

func (listener *Listener) handleQuery(b *bytes.Buffer, addr net.Addr) error {
	versonByte2, err := b.ReadByte()
	if err != nil {
		return fmt.Errorf("error query version byte 2: %v", err)
	}
	if versonByte2 != query.Header[1] {
		return nil
	}
	if err := listener.queryHandler.HandlePacket(b, addr); err != nil {
		return err
	}
	_, _ = listener.conn.WriteTo(b.Bytes(), addr)
	return nil
}
