package kcp

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"bytes"

	gosocks5 "github.com/ginuerzh/gosocks5"
	ss "github.com/shadowsocks/shadowsocks-go/shadowsocks"
	"github.com/yinghuocho/gotun2socks/core/packet"
)

var debug ss.DebugLog

var (
	errAddrType      = errors.New("socks addr type not supported")
	errVer           = errors.New("socks version not supported")
	errMethod        = errors.New("socks only support 1 method now")
	errAuthExtraData = errors.New("socks authentication get extra data")
	errReqExtraData  = errors.New("socks request get extra data")
	errCmd           = errors.New("socks command not supported")
)

const (
	socksVer5        = 5
	socksCmdConnect  = 1
	socksCmdConnect2 = 2
	socksCmdConnect3 = 3
)

func init() {
	rand.Seed(time.Now().Unix())
}

func handShake(conn net.Conn) (err error) {
	const (
		idVer     = 0
		idNmethod = 1
	)
	// version identification and method selection message in theory can have
	// at most 256 methods, plus version and nmethod field in total 258 bytes
	// the current rfc defines only 3 authentication methods (plus 2 reserved),
	// so it won't be such long in practice

	buf := make([]byte, 258)

	var n int
	ss.SetReadTimeout(conn)
	// make sure we get the nmethod field
	if n, err = io.ReadAtLeast(conn, buf, idNmethod+1); err != nil {
		return
	}
	if buf[idVer] != socksVer5 {
		return errVer
	}
	nmethod := int(buf[idNmethod])
	msgLen := nmethod + 2
	if n == msgLen { // handshake done, common case
		// do nothing, jump directly to send confirmation
	} else if n < msgLen { // has more methods to read, rare case
		if _, err = io.ReadFull(conn, buf[n:msgLen]); err != nil {
			return
		}
	} else { // error, should not get extra data
		return errAuthExtraData
	}
	// send confirmation: version 5, no authentication required
	_, err = conn.Write([]byte{socksVer5, 0})
	return
}

func getRequest(conn net.Conn) (rawaddr []byte, host string, err error, requestCmd byte) {
	const (
		idVer   = 0
		idCmd   = 1
		idType  = 3 // address type index
		idIP0   = 4 // ip addres start index
		idDmLen = 4 // domain address length index
		idDm0   = 5 // domain address start index

		typeIPv4 = 1 // type is ipv4 address
		typeDm   = 3 // type is domain address
		typeIPv6 = 4 // type is ipv6 address

		lenIPv4   = 3 + 1 + net.IPv4len + 2 // 3(ver+cmd+rsv) + 1addrType + ipv4 + 2port
		lenIPv6   = 3 + 1 + net.IPv6len + 2 // 3(ver+cmd+rsv) + 1addrType + ipv6 + 2port
		lenDmBase = 3 + 1 + 1 + 2           // 3 + 1addrType + 1addrLen + 2port, plus addrLen
	)
	// refer to getRequest in server.go for why set buffer size to 263
	buf := make([]byte, 263)
	var n int
	ss.SetReadTimeout(conn)
	// read till we get possible domain length field
	if n, err = io.ReadAtLeast(conn, buf, idDmLen+1); err != nil {
		return
	}
	// check version and cmd
	if buf[idVer] != socksVer5 {
		err = errVer
		return
	}
	if buf[idCmd] == socksCmdConnect2 {
		log.Println("cmd is 2")
	}
	if buf[idCmd] == socksCmdConnect3 {
		log.Println("cmd is 3")

		udp := packet.NewUDP()
		packet.ParseUDP(buf, udp)
		log.Println(";SrcPort:", udp.SrcPort, ";DstPort:", udp.DstPort, ";cmd:", buf[idCmd], ";auth", buf[2])
	}
	if buf[idCmd] == socksCmdConnect {
		//		log.Println("error,buf[idCmd]:", string(buf[idCmd]))
		log.Println("cmd is 1")

		tcp := packet.NewTCP()
		packet.ParseTCP(buf, tcp)
		log.Println("HeaderLength:", tcp.HeaderLength(), ";SrcPort:", tcp.SrcPort, ";DstPort:", tcp.DstPort, ";SYN:", tcp.SYN, ";FIN:", tcp.FIN,
			";cmd:", buf[idCmd], ";auth", buf[2])
		//		err = errCmd
		//		return
	}
	requestCmd = buf[idCmd]
	reqLen := -1
	switch buf[idType] {
	case typeIPv4:
		reqLen = lenIPv4
	case typeIPv6:
		reqLen = lenIPv6
	case typeDm:
		reqLen = int(buf[idDmLen]) + lenDmBase
	default:
		err = errAddrType
		return
	}

	if n == reqLen {
		// common case, do nothing
	} else if n < reqLen { // rare case
		if _, err = io.ReadFull(conn, buf[n:reqLen]); err != nil {
			return
		}
	} else {
		err = errReqExtraData
		return
	}

	rawaddr = buf[idType:reqLen]
	log.Println("buf[idType]:", buf[idType], ";reqLen:", reqLen, ";rawaddr:", string(rawaddr))
	if true {
		var host2 string
		switch buf[idType] {
		case typeIPv4:
			host = net.IP(buf[idIP0 : idIP0+net.IPv4len]).String()
			host2 = net.IPv4(buf[4], buf[5], buf[6], buf[7]).String()
		case typeIPv6:
			host = net.IP(buf[idIP0 : idIP0+net.IPv6len]).String()
		case typeDm:
			host = string(buf[idDm0 : idDm0+buf[idDmLen]])
		}
		port := binary.BigEndian.Uint16(buf[reqLen-2 : reqLen])
		host = net.JoinHostPort(host, strconv.Itoa(int(port)))
		log.Println("port:", port, ";host:", host, ";host2:", host2)
	}

	return
}

type ServerCipher struct {
	server string
	cipher *ss.Cipher
}

var servers struct {
	srvCipher []*ServerCipher
	failCnt   []int // failed connection count
}

func parseServerConfig(config *ss.Config) {
	hasPort := func(s string) bool {
		_, port, err := net.SplitHostPort(s)
		if err != nil {
			return false
		}
		return port != ""
	}

	if len(config.ServerPassword) == 0 {
		method := config.Method
		if config.Auth {
			method += "-auth"
		}
		// only one encryption table
		cipher, err := ss.NewCipher(method, config.Password)
		if err != nil {
			log.Fatal("Failed generating ciphers:", err)
		}
		srvPort := strconv.Itoa(config.ServerPort)
		srvArr := config.GetServerArray()
		n := len(srvArr)
		servers.srvCipher = make([]*ServerCipher, n)

		for i, s := range srvArr {
			if hasPort(s) {
				log.Println("ignore server_port option for server", s)
				servers.srvCipher[i] = &ServerCipher{s, cipher}
			} else {
				servers.srvCipher[i] = &ServerCipher{net.JoinHostPort(s, srvPort), cipher}
			}
		}
	} else {
		// multiple servers
		n := len(config.ServerPassword)
		servers.srvCipher = make([]*ServerCipher, n)

		cipherCache := make(map[string]*ss.Cipher)
		i := 0
		for _, serverInfo := range config.ServerPassword {
			if len(serverInfo) < 2 || len(serverInfo) > 3 {
				log.Fatalf("server %v syntax error\n", serverInfo)
			}
			server := serverInfo[0]
			passwd := serverInfo[1]
			encmethod := ""
			if len(serverInfo) == 3 {
				encmethod = serverInfo[2]
			}
			if !hasPort(server) {
				log.Fatalf("no port for server %s\n", server)
			}
			// Using "|" as delimiter is safe here, since no encryption
			// method contains it in the name.
			cacheKey := encmethod + "|" + passwd
			cipher, ok := cipherCache[cacheKey]
			if !ok {
				var err error
				cipher, err = ss.NewCipher(encmethod, passwd)
				if err != nil {
					log.Fatal("Failed generating ciphers:", err)
				}
				cipherCache[cacheKey] = cipher
			}
			servers.srvCipher[i] = &ServerCipher{server, cipher}
			i++
		}
	}
	servers.failCnt = make([]int, len(servers.srvCipher))
	for _, se := range servers.srvCipher {
		log.Println("available remote server", se.server)
	}
	return
}

func connectToServer(serverId int, rawaddr []byte, addr string) (remote *ss.Conn, err error) {
	se := servers.srvCipher[serverId]
	log.Println("se.server:", se.server, ";rawaddr:", string(rawaddr))
	log.Println("before connectToServer")
	remote, err = ss.DialWithRawAddr(rawaddr, se.server, se.cipher.Copy())
	log.Println("after connectToServer")
	if err != nil {
		log.Println("error connecting to shadowsocks server:", err)
		const maxFailCnt = 30
		if servers.failCnt[serverId] < maxFailCnt {
			servers.failCnt[serverId]++
		}
		return nil, err
	}
	//	debug.Printf("connected to %s via %s\n", addr, se.server)
	servers.failCnt[serverId] = 0
	return
}

// Connection to the server in the order specified in the config. On
// connection failure, try the next server. A failed server will be tried with
// some probability according to its fail count, so we can discover recovered
// servers.
func createServerConn(rawaddr []byte, addr string) (remote *ss.Conn, err error) {
	const baseFailCnt = 20
	n := len(servers.srvCipher)
	skipped := make([]int, 0)
	for i := 0; i < n; i++ {
		// skip failed server, but try it with some probability
		if servers.failCnt[i] > 0 && rand.Intn(servers.failCnt[i]+baseFailCnt) != 0 {
			skipped = append(skipped, i)
			continue
		}
		remote, err = connectToServer(i, rawaddr, addr)
		if err == nil {
			return
		}
	}
	// last resort, try skipped servers, not likely to succeed
	for _, i := range skipped {
		remote, err = connectToServer(i, rawaddr, addr)
		if err == nil {
			return
		}
	}
	return nil, err
}

func handleConnection(conn net.Conn) {
	if debug {
		debug.Printf("socks connect from %s\n", conn.RemoteAddr().String())
	}
	closed := false
	defer func() {
		if !closed {
			conn.Close()
		}
	}()

	var err error = nil
	if err = handShake(conn); err != nil {
		log.Println("socks handshake:", err)
		return
	}
	rawaddr, addr, err, requestCmd := getRequest(conn)
	if err != nil {
		log.Println("error getting request:", err)
		return
	}
	/**
	A.对于TCP CONNECT
	将请求分析后，将目标地址和 目标端口从请求中解析出来(无论请求中带的地址是否以域名方式发送过来，最终要将地址转换为IPV4的地址),然后使用connect()连接到目标地址中的目标端口中去，
	如果成功连接，那就向客户端发送回10个字节的信息,第一字节为5,第二字节为0,第三字节为0,第四字节为1,其它字节都为0.
	B.对于UDP ASSOCIATE(这个复杂很多了)
	将请求分析后，先保存好客户端的连接信息(客户端的IP和连接过来的源端口),然后本地创建一个UDP的socket,并将socket使用bind()绑入本地所有地址中的一个UDP端口中去，
	然后得到本地UDP绑定的IP和端口,创建一个10个字节的信息，返回给客户端去.第一字节为0x05,第二和第三字节都为0,第四字节为0x01(IPV4地址),第五位到第8位是UDP绑定的IP(以DWORD模式保存),
	第9位和第10位是UDP绑定的端口(以WORD模式保存).
	*/
	if requestCmd == 1 {
		doConnectSocket(conn, rawaddr, addr, closed)
	} else if requestCmd == 3 {
		doUdpSocket(conn, rawaddr, addr, closed)
	}

}

func errorReplySocks5(reason byte) []byte {
	return []byte{0x05, reason, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
}

type replayUDPst struct {
	udpAddr *net.UDPAddr
	header  []byte
}

type SockAddr struct {
	Host string
	Port int
}

func (addr *SockAddr) ByteArray() []byte {
	bytes := make([]byte, 6)
	copy(bytes[:4], net.ParseIP(addr.Host).To4())
	bytes[4] = byte(addr.Port >> 8)
	bytes[5] = byte(addr.Port % 256)
	return bytes
}

//func relayCheck(remoteIP net.IP) bool {
//	for _, ip := range GetRelayMapIPS(proxy.Info.relayServer) {
//		if bytes.Equal(remoteIP, ip) {
//			return true
//		}
//	}
//	return false
//}

//func GetRelayMapIPS(relayServer string) []net.IP {
//	relayMap.l.RLock()
//	defer relayMap.l.RUnlock()

//	return relayMap.m[relayServer]
//}

func doUdpSocket(conn net.Conn, rawaddr []byte, addr string, closed bool) {
	log.Println("start udp socket")
	//负责读取本地与远程过来的数据，只负责replay远程
	UDPConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		log.Printf("failed to ListenUDP: %v\n", err)
		conn.Write(errorReplySocks5(0x01)) // general SOCKS server failure
		return
	}
	//只负责replay本地
	udpConnToClient, err := net.ListenUDP("udp", nil)
	if err != nil {
		log.Printf("failed to ListenUDP: %v\n", err)
		conn.Write(errorReplySocks5(0x01)) // general SOCKS server failure
		return
	}
	ssConn := ss.NewSecurePacketConn(UDPConn, servers.srvCipher[0].cipher.Copy(), true) // force OTA on
	defer UDPConn.Close()
	defer udpConnToClient.Close()
	// RelayCheck
	remoteIP := conn.RemoteAddr().(*net.TCPAddr).IP
	log.Println("remoteIP:", remoteIP)
	//	host := conn.LocalAddr().(*net.TCPAddr).IP.String()
	host := remoteIP
	port := UDPConn.LocalAddr().(*net.UDPAddr).Port
	localaddr := SockAddr{host.String(), port}
	// reply command
	buf := make([]byte, 10)
	copy(buf, []byte{0x05, 0x00, 0x00, 0x01})
	copy(buf[4:], localaddr.ByteArray())
	conn.Write(buf)
	conn.(*net.TCPConn).SetKeepAlive(true)
	conn.(*net.TCPConn).SetKeepAlivePeriod(15 * time.Second)

	conn.SetDeadline(time.Time{})
	coneMap := make(map[string]*replayUDPst, 128)

	file, _ := UDPConn.File()
	SendMsg(strconv.Itoa(int(file.Fd())))

	time.Sleep(2 * time.Second)
	go handleUDP(conn, UDPConn, ssConn, udpConnToClient, coneMap)
	io.Copy(ioutil.Discard, conn)
}

func isUseOfClosedConn(err error) bool {
	operr, ok := err.(*net.OpError)
	return ok && operr.Err.Error() == "use of closed network connection"
}

var blockdomain []string

func isBlockDomain(domain string) bool {
	for i := 0; i < len(blockdomain); i++ {
		if strings.HasSuffix(domain, blockdomain[i]) {
			return true
		}
	}
	return false
}

const MAX_UDPBUF = 4096
const (
	idType  = 0 // address type index
	idIP0   = 1 // ip addres start index
	idDmLen = 1 // domain address length index
	idDm0   = 2 // domain address start index

	typeIPv4 = 1 // type is ipv4 address
	typeDm   = 3 // type is domain address
	typeIPv6 = 4 // type is ipv6 address

	lenIPv4     = 1 + net.IPv4len + 2 // 1addrType + ipv4 + 2port
	lenIPv6     = 1 + net.IPv6len + 2 // 1addrType + ipv6 + 2port
	lenDmBase   = 1 + 1 + 2           // 1addrType + 1addrLen + 2port, plus addrLen
	lenHmacSha1 = 10
)

//Port Restricted Cone(NAT)
/**
有数据包时，首先将数据全部读取，然后判断数据是从客户端还是远程目标传送过来的(在读取时可以得到是从什么地址和端口读取到数据的，然后比较上面第6步时我们保存了下来的客户端的连接信息)，
如果数据是从客户端读取过来的,我们要将UDP头去掉.例如我们读取到的Buffer,Buffer[3]是1时，UDP头就是10个字节长度,如果Buffer[3]是3的话,UDP头长度是7+Buffer[4].
例如我们得到UDP头是20位，我们接收到的Buffer是50位长度，那么我们发送到目标的数据包长度是30位，前20位不发送，只发送后面的30位.
如果数据是从远程目标发送来的,我们就要多发送多10位的UDP头,这10位的UDP头前三位都是0,第四位是0x01,第五到第八位是我们保存下来的客户端的IP,第9和第十位是客户端的端口.
如果我们接收到的Buffer长度是50,那么我们发送到客户端的数据就要加上10位的UDP头，也就是一共要发送60位字节长度的数据.
*/
func handleUDP(conn net.Conn, UDPConn *net.UDPConn, ssConn *ss.SecurePacketConn, udpConnToClient *net.UDPConn, coneMap map[string]*replayUDPst) {
	defer conn.Close()
	log.Println("start handle udp")
	var Transfer int64 //protect by sync/atomic

	for {
		buf := make([]byte, MAX_UDPBUF)
		UDPConn.SetReadDeadline(time.Now().Add(1 * time.Minute))
		n, udpAddr, err := UDPConn.ReadFromUDP(buf)
		if err != nil {
			if !isUseOfClosedConn(err) {
				//				log.Printf("[%s]fail read client udp: %v\n", s5.User, err)
				log.Printf("[%s]fail read client udp: %v\n", err)
			}
			return
		}
		buf = buf[:n]
		log.Println("receive:", string(buf[:]))
		log.Println("udpAddr.String():", udpAddr.String())
		//		rus := s5.getConeMap(udpAddr.String())
		rus := coneMap[udpAddr.String()]

		//		cipher := ssConn.Copy()
		//		iv := make([]byte, ssConn.GetIvLen())
		//		copy(iv, buf[:ssConn.GetIvLen()])
		//		if err = cipher.InitDecrypt(iv); err == nil {
		//		if rus != nil {
		if udpAddr.IP.String() != "127.0.0.1" {
			//收到的数据为加密数据
			//			return
			//		}
			//		if rus != nil {
			// reply udp data to client
			// log.Printf("[%s]%s reply udp data to client:[%q]\n", s5.User, udpAddr, buf)
			paraseResult, n := ssConn.ParseReadData(buf)

			reqLen := lenIPv4
			dstIP := net.IP(paraseResult[idIP0 : idIP0+net.IPv4len])
			dst := &net.UDPAddr{
				IP:   dstIP,
				Port: int(binary.BigEndian.Uint16(paraseResult[reqLen-2 : reqLen])),
			}
			//			log.Println("is to client", len(rus.header), ";paraseResult:", len(paraseResult), ";n:", n)
			log.Println("is to client", ";paraseResult:", len(paraseResult), ";n:", n, ";dstIP:", dstIP.String(), ";port:", dst)

			rus = coneMap[dst.String()]
			if rus != nil {
				log.Println("rus is not null,rus.udpAddr:", rus.udpAddr.String())
			}
			//			sendToClient := &net.UDPAddr{
			//				IP:   rus.udpAddr.IP,
			//				Port: rus.udpAddr.Port,
			//			}
			sendData := paraseResult[reqLen:n]
			log.Println("send to client data:", string(sendData[:]))

			log.Println("is to client", len(rus.header), ";")
			data := make([]byte, 0, len(rus.header)+len(sendData))
			data = append(data, rus.header...)
			data = append(data, sendData...)
			//			n, err := UDPConn.WriteToUDP(data, rus.udpAddr)
			log.Println("写入到本地udp，写入服务地址:", udpConnToClient.LocalAddr().String())
			n, err := udpConnToClient.WriteTo(data, rus.udpAddr)
			if err != nil {
				log.Println("发送数据失败：", err.Error())
			}
			log.Println("write to client end,n:", n)
			//			upTransferUdp(int64(len(buf)), udpAddr.String())
			atomic.AddInt64(&Transfer, int64(n))
		} else {
			//send udp data to server
			if buf[0] != 0x00 || buf[1] != 0x00 || buf[2] != 0x00 {
				continue // RSV,RSV,FRAG
			}
			udpHeader := make([]byte, 0, 10)
			addrtype := buf[3]
			var remote string
			var udpData []byte
			if addrtype == 0x01 { // 0x01: IP V4 address
				ip := net.IPv4(buf[4], buf[5], buf[6], buf[7])
				if !ip.IsGlobalUnicast() {
					continue
				}
				remote = fmt.Sprintf("%s:%d", ip.String(), int(buf[8])<<8+int(buf[9]))
				udpData = buf[10:]
				udpHeader = append(udpHeader, buf[:10]...)
			} else if addrtype == 0x03 { // 0x03: DOMAINNAME
				nmlen := int(buf[4]) // domain name length
				nmbuf := buf[5 : 5+nmlen+2]
				if isBlockDomain(string(nmbuf[:nmlen])) {
					continue
				}
				remote = fmt.Sprintf("%s:%d", nmbuf[:nmlen], int(nmbuf[nmlen])<<8+int(nmbuf[nmlen+1]))
				udpData = buf[8+nmlen:]
				udpHeader = append(udpHeader, buf[:8+nmlen]...)
			} else {
				continue // address type not supported
			}
			log.Println("udp remoteIP:", remote, ";proxy address:", servers.srvCipher[0].server)
			//			remote = "127.0.0.1:12948"
			//			remote = "192.168.0.47:434"
			remoteAddr, err := net.ResolveUDPAddr("udp", remote)
			if err != nil {
				//				log.Printf("[%s]fail resolve dns: %v\n", s5.User, err)
				log.Printf("[%s]fail resolve dns: %v\n", err)
				continue
			}
			log.Println("走kcp：", servers.srvCipher[0].server)
			//			dstAddr, err := net.ResolveUDPAddr("udp", servers.srvCipher[0].server)
			dstAddr, err := net.ResolveUDPAddr("udp", "192.168.0.47:434") //此处暂时未走kcp
			if err != nil {
				//				log.Printf("[%s]fail resolve dns: %v\n", s5.User, err)
				log.Printf("[%s]fail resolve dns: %v\n", err)
				continue
			}
			//log.Printf("[%s]send udp package to %s:[%q]\n", s5.User, remote, udpData)
			//			s5.addConeMap(&replayUDPst{udpAddr, udpHeader}, remoteAddr.String())
			//			udpAddr.IP = []byte("10.0.2.2")
			coneMap[remoteAddr.String()] = &replayUDPst{udpAddr, udpHeader}
			//			n, _ := UDPConn.WriteToUDP(udpData, remoteAddr)
			log.Println("before send to server:", string(udpData[:]))
			dgram := gosocks5.NewUDPDatagram(gosocks5.NewUDPHeader(0, 0, ToSocksAddr(remoteAddr)), udpData)
			b := bytes.Buffer{}
			dgram.Write(&b)

			n, _ := ssConn.WriteTo(b.Bytes()[3:], dstAddr)
			log.Println("after write udp")

			//			s5.upTransferUdp(int64(n), udpAddr.String())
			atomic.AddInt64(&Transfer, int64(n))
		}
	}
}
func ToSocksAddr(addr net.Addr) *gosocks5.Addr {
	host := "0.0.0.0"
	port := 0
	if addr != nil {
		h, p, _ := net.SplitHostPort(addr.String())
		host = h
		port, _ = strconv.Atoi(p)
	}
	return &gosocks5.Addr{
		Type: gosocks5.AddrIPv4,
		Host: host,
		Port: uint16(port),
	}
}

//func upTransferUdp(sum int64, udpAddr string) {
//	atomic.AddInt64(&Transfer, sum)
//	//	if s5.Info.logEnable {
//	//		s5.OnceTcpId.Do(func() { <-s5.ChTcpId })

//	//		if s5.TcpId > 0 {
//	//			go InsertUpdateUdpLog(s5.TcpId, udpAddr, sum)
//	//		}
//	//	}
//}

func doConnectSocket(conn net.Conn, rawaddr []byte, addr string, closed bool) {
	// Sending connection established message immediately to client.
	// This some round trip time for creating socks connection with the client.
	// But if connection failed, the client will get connection reset error.
	_, err = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x08, 0x43})
	if err != nil {
		debug.Println("send connection confirmation:", err)
		return
	}

	remote, err := createServerConn(rawaddr, addr)
	if err != nil {
		if len(servers.srvCipher) > 1 {
			log.Println("Failed connect to all avaiable shadowsocks server")
		}
		return
	}
	//	file, _ := remote.Conn.(*net.TCPConn).File()
	//	log.Println("fd:", strconv.Itoa(int(file.Fd())))
	//	SendMsg(strconv.Itoa(int(file.Fd())))
	defer func() {
		if !closed {
			remote.Close()
		}
	}()

	go ss.PipeThenClose(conn, remote)
	ss.PipeThenClose(remote, conn)
	closed = true
	debug.Println("closed connection to", addr)
}

var shadowFd int

func GetShadowFd() int {
	return shadowFd
}

func run(listenAddr string) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("starting local socks5 server at %v ...\n", listenAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println("accept:", err)
			continue
		}
		go handleConnection(conn)
	}
}

func enoughOptions(config *ss.Config) bool {
	return config.Server != nil && config.ServerPort != 0 &&
		config.LocalPort != 0 && config.Password != ""
}

func StartShadowSocks() {
	//	log.SetOutput(os.Stdout)

	var configFile, cmdServer, cmdLocal string
	var cmdConfig ss.Config
	var printVer bool

	flag.BoolVar(&printVer, "version", false, "print version")
	flag.StringVar(&configFile, "c", "config.json", "specify config file")
	flag.StringVar(&cmdServer, "s", "127.0.0.1", "server address")
	//	flag.StringVar(&cmdServer, "s", "192.168.0.47", "server address")
	flag.StringVar(&cmdLocal, "b", "", "local address, listen only to this address if specified")
	flag.StringVar(&cmdConfig.Password, "k", "ODA5MzVjYj", "password")
	flag.IntVar(&cmdConfig.ServerPort, "p", 12948, "server port")
	//	flag.IntVar(&cmdConfig.ServerPort, "p", 434, "server port")
	flag.IntVar(&cmdConfig.Timeout, "t", 300, "timeout in seconds")
	flag.IntVar(&cmdConfig.LocalPort, "l", 1080, "local socks5 proxy port")
	flag.StringVar(&cmdConfig.Method, "m", "chacha20", "encryption method, default: aes-256-cfb")
	flag.BoolVar((*bool)(&debug), "d", true, "print debug message")
	flag.BoolVar(&cmdConfig.Auth, "A", false, "one time auth")
	flag.BoolVar(&cmdConfig.UDP, "U", true, "是否支持udp")

	flag.Parse()

	if printVer {
		ss.PrintVersion()
		os.Exit(0)
	}

	cmdConfig.Server = cmdServer
	ss.SetDebug(debug)

	if strings.HasSuffix(cmdConfig.Method, "-auth") {
		cmdConfig.Method = cmdConfig.Method[:len(cmdConfig.Method)-5]
		cmdConfig.Auth = true
	}

	exists, err := ss.IsFileExists(configFile)
	// If no config file in current directory, try search it in the binary directory
	// Note there's no portable way to detect the binary directory.
	binDir := path.Dir(os.Args[0])
	if (!exists || err != nil) && binDir != "" && binDir != "." {
		oldConfig := configFile
		configFile = path.Join(binDir, "config.json")
		log.Printf("%s not found, try config file %s\n", oldConfig, configFile)
	}

	config, err := ss.ParseConfig(configFile)
	if err != nil {
		config = &cmdConfig
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "error reading %s: %v\n", configFile, err)
			os.Exit(1)
		}
	} else {
		ss.UpdateConfig(config, &cmdConfig)
	}
	if config.Method == "" {
		config.Method = "aes-256-cfb"
	}
	if len(config.ServerPassword) == 0 {
		if !enoughOptions(config) {
			fmt.Fprintln(os.Stderr, "must specify server address, password and both server/local port")
			os.Exit(1)
		}
	} else {
		if config.Password != "" || config.ServerPort != 0 || config.GetServerArray() != nil {
			fmt.Fprintln(os.Stderr, "given server_password, ignore server, server_port and password option:", config)
		}
		if config.LocalPort == 0 {
			fmt.Fprintln(os.Stderr, "must specify local port")
			os.Exit(1)
		}
	}

	parseServerConfig(config)

	run(cmdLocal + ":" + strconv.Itoa(config.LocalPort))
}
