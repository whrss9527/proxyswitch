package main

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// 开发模式和测试用的假代理：一个 HTTP 代理（CONNECT 隧道 + 普通转发）、一个 SOCKS5 代理，
// 以及返回 204 的测速地址。让检测、测速、健康检查在没有真实代理软件的环境里也能完整运行。

type fakeProxy struct {
	mutex    sync.Mutex
	address  string
	delay    time.Duration
	serve    func(connection net.Conn, delay time.Duration)
	listener net.Listener
}

func startFakeProxy(serve func(net.Conn, time.Duration), delay time.Duration) (*fakeProxy, error) {
	proxy := &fakeProxy{address: "127.0.0.1:0", delay: delay, serve: serve}
	if err := proxy.Start(); err != nil {
		return nil, err
	}
	return proxy, nil
}

// Start 开始监听；停止后再次调用会复用原来的端口。
func (proxy *fakeProxy) Start() error {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	if proxy.listener != nil {
		return nil
	}
	listener, err := net.Listen("tcp", proxy.address)
	if err != nil {
		return err
	}
	proxy.listener = listener
	proxy.address = listener.Addr().String()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go proxy.serve(connection, proxy.delay)
		}
	}()
	return nil
}

func (proxy *fakeProxy) Stop() {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	if proxy.listener != nil {
		_ = proxy.listener.Close()
		proxy.listener = nil
	}
}

func (proxy *fakeProxy) Address() string {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	return proxy.address
}

func (proxy *fakeProxy) Port() int {
	_, port, _ := net.SplitHostPort(proxy.Address())
	number, _ := strconv.Atoi(port)
	return number
}

func serveFakeHttpProxy(client net.Conn, delay time.Duration) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(client)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	time.Sleep(delay)
	if request.Method == http.MethodConnect {
		upstream, err := net.DialTimeout("tcp", request.Host, 5*time.Second)
		if err != nil {
			_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nProxy-Agent: ProxySwitch-dev\r\nContent-Length: 0\r\n\r\n")
			return
		}
		defer upstream.Close()
		_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\nProxy-Agent: ProxySwitch-dev\r\n\r\n")
		relay(client, reader, upstream)
		return
	}
	host := request.URL.Host
	if request.URL.Port() == "" {
		host = net.JoinHostPort(request.URL.Hostname(), "80")
	}
	upstream, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nProxy-Agent: ProxySwitch-dev\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer upstream.Close()
	request.Header.Del("Proxy-Connection")
	request.Close = true
	if err := request.Write(upstream); err != nil {
		return
	}
	response, err := http.ReadResponse(bufio.NewReader(upstream), request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	response.Header.Set("Proxy-Agent", "ProxySwitch-dev")
	_ = response.Write(client)
}

func serveFakeSocksProxy(client net.Conn, delay time.Duration) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(client)
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(reader, greeting); err != nil || greeting[0] != 5 {
		return
	}
	if _, err := io.ReadFull(reader, make([]byte, greeting[1])); err != nil {
		return
	}
	if _, err := client.Write([]byte{5, 0}); err != nil {
		return
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil || header[1] != 1 {
		return
	}
	var host string
	switch header[3] {
	case 1:
		address := make([]byte, 4)
		if _, err := io.ReadFull(reader, address); err != nil {
			return
		}
		host = net.IP(address).String()
	case 4:
		address := make([]byte, 16)
		if _, err := io.ReadFull(reader, address); err != nil {
			return
		}
		host = net.IP(address).String()
	case 3:
		length, err := reader.ReadByte()
		if err != nil {
			return
		}
		name := make([]byte, length)
		if _, err := io.ReadFull(reader, name); err != nil {
			return
		}
		host = string(name)
	default:
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return
	}
	time.Sleep(delay)
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes))))
	upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		_, _ = client.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err := client.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	relay(client, reader, upstream)
}

// relay 在客户端与上游之间双向转发，客户端一侧先读完缓冲里剩下的数据。
func relay(client net.Conn, clientReader *bufio.Reader, upstream net.Conn) {
	_ = client.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, clientReader)
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(connection net.Conn) {
	if tcpConnection, ok := connection.(*net.TCPConn); ok {
		_ = tcpConnection.CloseWrite()
	}
}

// startFakeTestServer 启动一个返回 204 的测速地址，同一个服务的 /proxy.pac 是一个 PAC 脚本。
func startFakeTestServer() (string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/proxy.pac" {
				writer.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
				_, _ = io.WriteString(writer, "function FindProxyForURL(url, host) {\n  return \"DIRECT\";\n}\n")
				return
			}
			writer.WriteHeader(http.StatusNoContent)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	return "http://" + listener.Addr().String() + "/generate_204", func() { _ = server.Close() }, nil
}
