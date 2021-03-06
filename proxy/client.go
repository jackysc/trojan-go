package proxy

import (
	"net"
	"sync"

	"github.com/p4gefau1t/trojan-go/common"
	"github.com/p4gefau1t/trojan-go/conf"
	"github.com/p4gefau1t/trojan-go/protocol"
	"github.com/p4gefau1t/trojan-go/protocol/socks"
	"github.com/p4gefau1t/trojan-go/protocol/trojan"
	"github.com/xtaci/smux"
)

type muxConn struct {
	conn protocol.ConnSession
	sync.Mutex
}

type Client struct {
	config *conf.GlobalConfig
	common.Runnable
	muxClient     *smux.Session
	muxClientLock sync.Mutex
}

func (c *Client) checkAndNewMuxClient() {
	if c.muxClient == nil || c.muxClient.IsClosed() {
		c.muxClientLock.Lock()
		defer c.muxClientLock.Unlock()
		if c.muxClient != nil && !c.muxClient.IsClosed() {
			//it has been build by other goroutine
			return
		}
		//mux request
		req := &protocol.Request{
			Command:     protocol.Mux,
			IP:          net.IPv4(233, 233, 233, 234),
			Port:        2333,
			AddressType: protocol.IPv4,
		}
		conn, err := trojan.NewOutboundConnSession(req, nil, c.config)
		if err != nil {
			logger.Error(common.NewError("failed to dial mux conn").Base(err))
			return
		}
		logger.Info("mux TLS tunnel established")
		client, err := smux.Client(conn, nil)
		common.Must(err)
		c.muxClient = client
	}
}

func (c *Client) proxyToMuxConn(req *protocol.Request, conn protocol.ConnSession) {
	stream, err := c.muxClient.OpenStream()
	if err != nil {
		logger.Error(err)
		c.muxClient.Close()
		return
	}
	defer stream.Close()
	//trojan protocol over mux conn
	outbound, err := trojan.NewOutboundConnSession(req, stream, c.config)
	if err != nil {
		err = common.NewError("fail to start trojan session over mux conn").Base(err)
		logger.Error(err)
		return
	}
	defer outbound.Close()
	logger.Info("mux tunneling to", req)
	proxyConn(conn, outbound)
}

func (c *Client) handleMuxConn(conn net.Conn) {
	inboundConn, err := socks.NewInboundConnSession(conn)
	if err != nil {
		logger.Error("failed to start new inbound session:", err)
		return
	}
	defer inboundConn.Close()
	req := inboundConn.GetRequest()
	c.checkAndNewMuxClient()
	if req.Command == protocol.Associate {
		//not using mux
		outboundConn, err := trojan.NewOutboundConnSession(req, nil, c.config)
		if err != nil {
			logger.Error("failed to start new outbound session:", err)
			return
		}
		defer outboundConn.Close()
		listenConn, err := net.ListenUDP("udp", &net.UDPAddr{
			IP: c.config.LocalIP,
		})
		if err != nil {
			logger.Error("failed to listen udp:", err)
			return
		}

		req.IP = c.config.LocalIP
		port, err := protocol.ParsePort(listenConn.LocalAddr())
		common.Must(err)
		req.Port = uint16(port)
		req.AddressType = protocol.IPv4

		inboundPacket, err := socks.NewInboundPacketSession(listenConn)
		if err != nil {
			logger.Error("failed to start inbound packet session:", err)
			return
		}
		defer inboundPacket.Close()

		outboundPacket, _ := trojan.NewPacketSession(outboundConn)
		go proxyPacket(inboundPacket, outboundPacket)

		inboundConn.(protocol.NeedRespond).Respond(nil)
		logger.Info("UDP associated to", req.String())

		var buf [1]byte
		_, err = conn.Read(buf[:])
		logger.Info("UDP conn ends")
		return
	}

	if err := inboundConn.(protocol.NeedRespond).Respond(nil); err != nil {
		logger.Error("failed to respond:", err)
		return
	}

	logger.Info("conn from", conn.RemoteAddr(), "tunneling to", req)
	c.proxyToMuxConn(req, inboundConn)
}

func (c *Client) handleConn(conn net.Conn) {
	inboundConn, err := socks.NewInboundConnSession(conn)
	if err != nil {
		logger.Error("failed to start new inbound session:", err)
		return
	}
	defer inboundConn.Close()
	req := inboundConn.GetRequest()

	outboundConn, err := trojan.NewOutboundConnSession(req, nil, c.config)
	if err != nil {
		logger.Error("failed to start new outbound session:", err)
		return
	}
	defer outboundConn.Close()

	if req.Command == protocol.Associate {
		listenConn, err := net.ListenUDP("udp", &net.UDPAddr{
			IP: c.config.LocalIP,
		})
		if err != nil {
			logger.Error("failed to listen udp:", err)
			return
		}

		req.IP = c.config.LocalIP
		port, err := protocol.ParsePort(listenConn.LocalAddr())
		common.Must(err)
		req.Port = uint16(port)
		req.AddressType = protocol.IPv4

		inboundPacket, err := socks.NewInboundPacketSession(listenConn)
		if err != nil {
			logger.Error("failed to start inbound packet session:", err)
			return
		}
		defer inboundPacket.Close()

		outboundPacket, _ := trojan.NewPacketSession(outboundConn)
		go proxyPacket(inboundPacket, outboundPacket)

		inboundConn.(protocol.NeedRespond).Respond(nil)
		logger.Info("UDP associated to", req.String())

		var buf [1]byte
		_, err = conn.Read(buf[:])
		logger.Info("UDP conn ends")
		return
	}

	if err := inboundConn.(protocol.NeedRespond).Respond(nil); err != nil {
		logger.Error("failed to respond:", err)
		return
	}

	logger.Info("conn from", conn.RemoteAddr(), "tunneling to", req)
	proxyConn(inboundConn, outboundConn)
}

func (c *Client) Run() error {
	listener, err := net.Listen("tcp", c.config.LocalAddr.String())
	if err != nil {
		return err
	}
	logger.Info("running client at", listener.Addr())
	for {
		conn, err := listener.Accept()
		if err != nil {
			logger.Error("error occured when accpeting conn", err)
			continue
		}
		if c.config.TCP.Mux {
			go c.handleMuxConn(conn)
		} else {
			go c.handleConn(conn)
		}
	}
}
