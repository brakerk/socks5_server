package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
)

const (
	RequestAccepted = iota
	SocksError
	ConnDeniedByRules
	NetUnreachable
	HostUnreachable
	ConnectionRefused
	TTLExpired
	CommandNotSupported
	AddrTypeNotSupported
)

type SocksServer struct {
}

type Request struct {
	Version  byte
	Command  byte
	AddrType byte
	IP4Addr  []byte
	IP6Addr  []byte
	Host     []byte
	Port     int
}

type Response struct {
	Version      byte
	ResponseCode byte
	AddrType     byte
	AddrDest     []byte
	Port         []byte
}

func (s *SocksServer) error(conn net.Conn, respCode byte) error {

	resp := &Response{
		Version:      0x5,
		ResponseCode: respCode,
		AddrType:     0x1,
		AddrDest:     []byte{0, 0, 0, 0},
		Port:         []byte{0, 0},
	}

	buf := []byte{resp.Version, resp.ResponseCode, 0, resp.AddrType}

	buf = append(buf, resp.AddrDest...)
	buf = append(buf, resp.Port...)

	_, err := conn.Write(buf)
	return err
}

func (s *SocksServer) proxy(dst io.Writer, src io.Reader, errCh chan error) {
	_, err := io.Copy(dst, src)
	if closer, ok := dst.(*net.TCPConn); ok {
		closer.CloseWrite()
	}
	errCh <- err
}

func (s *SocksServer) respond(conn net.Conn, ip net.IP, port int) error {

	var addrType int
	var addr []byte
	if ip.To4() != nil {
		addrType = 0x1
		addr = ip.To4()
	} else if ip.To16() != nil {
		addrType = 0x4
		addr = ip.To16()
	} else {
		return s.error(conn, AddrTypeNotSupported)
	}

	resp := Response{
		Version:      0x5,
		ResponseCode: RequestAccepted,
		AddrType:     byte(addrType),
		AddrDest:     addr,
		Port:         []byte{byte(port << 8), byte(port & 0xff)},
	}

	buff := []byte{resp.Version, resp.ResponseCode, 0, resp.AddrType}

	buff = append(buff, resp.AddrDest...)
	buff = append(buff, resp.Port...)

	_, err := conn.Write(buff)

	return err
}

func (s *SocksServer) connect(request *Request, conn net.Conn) error {

	var addr string

	switch request.AddrType {
	case 0x1:
		addr = net.IPv4(
			request.IP4Addr[0],
			request.IP4Addr[1],
			request.IP4Addr[2],
			request.IP4Addr[4],
		).String()
	case 0x4:
		addr = net.ParseIP(string(request.IP6Addr)).String()
	case 0x3:
		ip, err := net.ResolveIPAddr("ip", string(request.Host))
		if err != nil {
			return err
		}
		addr = ip.String()
	}

	target, err := net.Dial("tcp", net.JoinHostPort(addr, strconv.Itoa(request.Port)))

	if err != nil {
		return err
	}

	defer target.Close()

	local := target.LocalAddr().(*net.TCPAddr)

	err = s.respond(conn, local.IP, local.Port)
	if err != nil {
		return err
	}

	errCh := make(chan error)

	go s.proxy(target, conn, errCh)
	go s.proxy(conn, target, errCh)

	err = <-errCh
	if err != nil {
		return err
	}
	err = <-errCh
	if err != nil {
		return err
	}

	return nil
}

func (s *SocksServer) request(conn net.Conn, bConn *bufio.Reader) error {

	request := &Request{}

	header := make([]byte, 4)

	_, err := io.ReadFull(bConn, header)

	if err != nil {
		return err
	}

	request.Version = header[0]
	request.Command = header[1]
	request.AddrType = header[3]

	addrLen := 0
	switch request.AddrType {
	case 0x1:
		addrLen = 4
	case 0x4:
		addrLen = 16
	case 0x3:
		length, err := bConn.ReadByte()
		if err != nil {
			return err
		}
		addrLen = int(length)
	default:
		return s.error(conn, AddrTypeNotSupported)
	}

	portBytes := 2
	addrBuf := make([]byte, addrLen+portBytes)
	_, err = io.ReadFull(bConn, addrBuf)
	if err != nil {
		return err
	}

	switch request.AddrType {
	case 0x1:
		request.IP4Addr = addrBuf[:addrLen]
	case 0x4:
		request.IP6Addr = addrBuf[:addrLen]
	case 0x3:
		request.Host = addrBuf[:addrLen]
	}

	request.Port = int(addrBuf[addrLen])>>8 | int(addrBuf[addrLen+1])

	switch request.Command {
	case 0x1:
		return s.connect(request, conn)
	default:
		return s.error(conn, CommandNotSupported)
	}

}

func (s *SocksServer) handleConnection(conn net.Conn) error {

	defer conn.Close()

	bConn := bufio.NewReader(conn)

	header := []byte{0, 0}
	_, err := io.ReadFull(bConn, header)
	if err != nil {
		return err
	}
	if header[0] != 0x5 {
		return errors.New("version not supported")
	}
	//TODO - error
	authTypeLen := int(header[1])
	authTypes := make([]byte, authTypeLen)

	_, err = io.ReadFull(bConn, authTypes)
	if err != nil {
		return err
	}

	log.Printf("auth types := %+v", authTypes)

	_, err = conn.Write([]byte{0x5, 0})
	if err != nil {
		return err
	}

	return s.request(conn, bConn)
}

func (s *SocksServer) ListenAndServe(network, addr string) error {

	var err error
	l, err := net.Listen(network, addr)

	if err != nil {
		return err
	}

	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}

		err = s.handleConnection(conn)

		if err != nil {
			log.Printf("conn error: %v", err)
		}

	}

}

func main() {
	port := flag.Int("port", 1080, "port for proxy")
	flag.Parse()
	server := &SocksServer{}

	err := server.ListenAndServe("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatal(err)
	}
}
