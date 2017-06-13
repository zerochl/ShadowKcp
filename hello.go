// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package hello is a trivial package for gomobile bind example.
package kcp

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strings"
	"time"

	//	"strconv"
	"zerolib/udp/udplotus"

	"github.com/gamexg/proxyclient"
)

func Greetings(name string) string {
	for i := 0; i < 2; i++ {
		time.Sleep(2 * time.Second)
		log.Println("sleep:", i)
	}
	return fmt.Sprintf("HelloVeryGood, %s!", name)
}

func SendMsg(msg string) {
	//	conn, err := net.DialTimeout("tcp", "127.0.0.1:1090", 1000*1000*1000*30)
	conn, err := net.Dial("tcp", "127.0.0.1:1090")
	if err != nil {
		fmt.Printf("create client err:%s\n", err)
		return
	}
	defer conn.Close()
	senddata := []byte(msg)
	_, err = conn.Write(senddata)
	if err != nil {
		log.Println("send msg err:", err)
	}
}

func SendUdpMsg() {
	// 创建连接
	socket, err = net.DialUDP("udp4", nil, &net.UDPAddr{
		IP: net.IPv4(192, 168, 0, 47),
		//		IP:   net.IPv4(104, 224, 174, 229),
		Port: 1082,
	})
	if err != nil {
		log.Println("udp连接失败!", err)
		return
	}
	//	file, _ := socket.File()
	//	SendMsg(strconv.Itoa(int(file.Fd())))
	defer socket.Close()
	log.Println("udp连接成功!port:", socket.LocalAddr().String())
	senddata := []byte("我日")
	//	file, _ := socket.File()
	//	SendMsg(strconv.Itoa(int(file.Fd())))
	time.Sleep(2 * time.Second)
	_, err = socket.Write(senddata)
	if err != nil {
		log.Println("send msg err:", err)
	}
	getdata := make([]byte, 1024)
	length, _, _ := socket.ReadFromUDP(getdata)
	log.Println("udp result,length:", length, ";", string(getdata[:length]))
}

func DialUrl() {
	server, err := net.Dial("tcp", "www.baidu.com:443")
	//	server, err := net.Dial("tcp", "google.com.hk:443")
	if err != nil {
		log.Println("err")
	}
	buf := make([]byte, 8196)
	log.Println("before read")
	server.Write(buf)
	length, _ := server.Read(buf)
	log.Println("after read")
	log.Println("body:", string(buf[:length]))
}

var socket *net.UDPConn
var err error

func createVpnClient() {
	// 创建连接
	socket, err = net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 0,
	})
	if err != nil {
		log.Println("连接失败!", err)
		return
	}
	log.Println("连接成功!")
	//	log.Println(socket.LocalAddr())
}

func StartVPNServer3() {
	log.Println("start vpn server3")
	//start tcp server
	socket2, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 1082,
	})
	checkError(err)
	server, err := net.Dial("tcp", "127.0.0.1:1082")
	checkError(err)
	data := make([]byte, 4096)
	for {
		//进行转发
		length, addr, err := socket2.ReadFromUDP(data)
		if err != nil {
			log.Println("error:", err, ";addr:", addr)
			continue
		}
		if length <= 0 {
			continue
		}
		log.Println("不为0，执行copy,addr:", addr)
		go io.Copy(server, socket2)
		io.Copy(socket2, server)
	}
}

func handleClientRequest2(client net.Conn) {
	if client == nil {
		return
	}
	defer client.Close()
	var b [1024]byte
	for {
		_, err := client.Read(b[:])
		if err != nil {
			log.Println(err)
			return
		}

		//	method, address := getAddress(b)
		address := string(b[8]) + "." + string(b[9]) + "." + string(b[10]) + "." + string(b[11]) + ":443"
		log.Println("address:", address)
		p, err := proxyclient.NewProxyClient("socks5://@127.0.0.1:1080")
		//	log.Println("address:", address)
		//获得了请求的host和port，就开始拨号吧
		server, err := p.Dial("tcp", address)
		if err != nil {
			log.Println(err)
			return
		}
		fmt.Fprint(client, "HTTP/1.1 200 Connection established\r\n\r\n")
		//	if method == "CONNECT" {
		//		fmt.Fprint(client, "HTTP/1.1 200 Connection established\r\n\r\n")
		//	} else {
		//		server.Write(b[:n])
		//	}
		//进行转发
		go io.Copy(server, client)
		io.Copy(client, server)
	}

}

func StartVPNServer2() {
	// 创建监听
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 1082,
	})
	if err != nil {
		log.Println("监听失败!", err)
		return
	}
	defer socket.Close()
	for {
		// 读取数据
		data := make([]byte, 4096)
		log.Println("before read")
		//		server, err := net.DialTCP("tcp", nil, &net.TCPAddr{
		//			IP:   net.IPv4(127, 0, 0, 1),
		//			Port: 1081,
		//		})
		//		if err != nil {
		//			log.Println("创建server失败", err)
		//			continue
		//		}
		//		//进行转发
		//		go io.Copy(server, socket)
		//		io.Copy(socket, server)
		read, remoteAddr, err := socket.ReadFromUDP(data)

		log.Println("after read")
		if err != nil {
			log.Println("读取数据失败!", err)
			continue
		}
		log.Println("服务器接收数据", remoteAddr)
		log.Println("%s", string(data[:read]))
		// 发送数据
		senddata := []byte("hello client,1024")
		_, err = socket.WriteToUDP(senddata, remoteAddr)
		if err != nil {
			return
			log.Println("发送数据失败!", err)
		}

	}
}

func GetSocketId() int {
	if socket == nil {
		createVpnClient()
	}
	file, _ := socket.File()
	return int(file.Fd())
	//	return socket.GetFD()
}

func GetLocalAddr() string {
	if socket == nil {
		createVpnClient()
	}
	localAddr := socket.LocalAddr().String()
	return localAddr
}

func GetLocalPort() string {
	if socket == nil {
		createVpnClient()
	}
	localAddr := socket.LocalAddr().String()
	lastIndex := strings.LastIndex(localAddr, ":")
	return localAddr[lastIndex+1:]
}

func Close() {
	if socket == nil {
		return
	}
	socket.Close()
}

func WriteVpn(msg []byte, length int) {

	//	defer socket.Close()
	// 发送数据
	//	senddata := []byte(msg)
	if socket == nil {
		createVpnClient()
	}

	log.Println("写入的数据：", ";length:", length, ";data:", string(msg[:length]))
	//	senddata := []byte("20481")
	_, err = socket.Write(msg[:length])
	if err != nil {
		log.Println("发送数据失败!", err)
		return
	}
}

func ReadVpn(data []byte) int {

	if socket == nil {
		createVpnClient()
	}
	// 接收数据
	//	data := make([]byte, 4096)
	read, remoteAddr, err := socket.ReadFromUDP(data)
	if err != nil {
		log.Println("读取数据失败!", err)
		return 0
	}
	log.Println(read, remoteAddr)
	log.Println("读取数据,remoteAddr:", remoteAddr, ";length:", read, ";data:", string(data[3]))
	return read
}

func StartVPNServer() {
	udplotus.UdpLotusMain("127.0.0.1", 1082)
}

func TestRequest() {
	p, err := proxyclient.NewProxyClient("socks5://@127.0.0.1:1080")
	if err != nil {
		log.Println("err")
	}
	server, err := p.Dial("tcp", "www.baidu.com:443")
	if err != nil {
		log.Println("err")
	}
	buf := make([]byte, 8196)
	log.Println("before write in TestRequest")
	server.Write(buf)
	length, _ := server.Read(buf)
	log.Println("after read")
	log.Println("body:", string(buf[:length]))
}

func TestRequest2() {
	server, err := net.Dial("tcp", "www.baidu.com:443")
	if err != nil {
		log.Println("err")
	}
	buf := make([]byte, 8196)
	log.Println("before read")
	server.Write(buf)
	length, _ := server.Read(buf)
	log.Println("after read")
	log.Println("body:", string(buf[:length]))
}

func StartHttpProxy() {
	l, err := net.Listen("tcp", "127.0.0.1:1081")
	if err != nil {
		log.Panic(err)
	}
	for {
		client, err := l.Accept()
		if err != nil {
			log.Panic(err)
		}
		go handleClientRequest(client)
	}
}

func handleClientRequest(client net.Conn) {
	if client == nil {
		return
	}
	defer client.Close()
	var b [1024]byte
	n, err := client.Read(b[:])
	if err != nil {
		log.Println(err)
		return
	}
	method, address := getAddress(b)
	p, err := proxyclient.NewProxyClient("socks5://@127.0.0.1:1080")
	log.Println("address:", address)
	//获得了请求的host和port，就开始拨号吧
	server, err := p.Dial("tcp", address)
	if err != nil {
		log.Println(err)
		return
	}
	if method == "CONNECT" {
		fmt.Fprint(client, "HTTP/1.1 200 Connection established\r\n\r\n")
	} else {
		server.Write(b[:n])
	}
	//进行转发
	go io.Copy(server, client)
	io.Copy(client, server)
}

func getAddress(b [1024]byte) (method string, address string) {
	var host string
	log.Println("head:", string(b[:bytes.IndexByte(b[:], '\n')]))
	fmt.Sscanf(string(b[:bytes.IndexByte(b[:], '\n')]), "%s%s", &method, &host)
	//	log.Println("host:", host)
	hostPortURL, err := url.Parse(host)
	if err != nil {
		log.Println(err)
		return "", ""
	}
	if hostPortURL.Opaque == "443" { //https访问
		address = hostPortURL.Scheme + ":443"
	} else { //http访问
		//		log.Println("hostPortURL.Host:", hostPortURL.Host, ";hostPortURL.Scheme:", hostPortURL.Scheme)
		if hostPortURL.Host == "" {
			address = host
		} else if strings.Index(hostPortURL.Host, ":") == -1 { //host不带端口， 默认80
			address = hostPortURL.Host + ":80"
		} else {
			address = hostPortURL.Host
		}
	}

	return method, address
}
