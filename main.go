package main

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"context"

	"math/rand"

	"github.com/armon/go-socks5"
	"github.com/gorilla/websocket"
)

type Config struct {
	Domain    string   `json:"domain"`
	PSW       string   `json:"psw"`
	Sport     int      `json:"sport"`
	SBind     string   `json:"sbind"`
	WKIP      string   `json:"wkip"`
	WKPort    int      `json:"wkport"`
	ProxyIP   string   `json:"proxyip"`
	ProxyPort int      `json:"proxyport"`
	CFHS      []string `json:"cfhs"`
}

var (
	config  Config
	cache   = make(map[string]bool)
	CFDOMAN = []string{}
)

var CIDR4 = []string{"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20",
	"188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22"}
var CIDR6 = []string{"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32"}

var ADDR4 []struct {
	m uint32
	a uint32
}

var ADDR6 []struct {
	m int
	s string
}

func init() {
	for _, cidr := range CIDR4 {
		parts := strings.Split(cidr, "/")
		addr := net.ParseIP(parts[0]).To4()
		mask, _ := strconv.Atoi(parts[1])
		m := uint32((1<<uint32(32-mask) - 1) ^ 0xffffffff)
		a := binary.BigEndian.Uint32(addr)
		ADDR4 = append(ADDR4, struct {
			m uint32
			a uint32
		}{m, a})
	}

	for _, cidr := range CIDR6 {
		parts := strings.Split(cidr, "/")
		addr := net.ParseIP(parts[0]).To16()
		mask, _ := strconv.Atoi(parts[1])
		s := ""
		for _, b := range addr {
			s += fmt.Sprintf("%08b", b)
		}
		s = s[:mask]
		ADDR6 = append(ADDR6, struct {
			m int
			s string
		}{mask, s})
	}
}

func ipInCFCidr(ip string) (bool, string) {
	if strings.Contains(ip, ":") {
		// IPv6
		addr := net.ParseIP(ip).To16()
		s := ""
		for _, b := range addr {
			s += fmt.Sprintf("%08b", b)
		}
		for _, a6 := range ADDR6 {
			if s[:a6.m] == a6.s {
				return true, ip
			}
		}
	} else {
		// IPv4
		addr := net.ParseIP(ip).To4()
		if addr == nil {
			return false, ip
		}
		a := binary.BigEndian.Uint32(addr)
		for _, a4 := range ADDR4 {
			if (a & a4.m) == (a4.a & a4.m) {
				return true, ip
			}
		}
	}
	return false, ip
}

func dns(host string) (string, error) {
	url := fmt.Sprintf("https://cloudflare-dns.com/dns-query?name=%s&type=A", host)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Add("Accept", "application/dns-json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Status int `json:"Status"`
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}

	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return "", err
	}

	if result.Status == 0 && len(result.Answer) > 0 {
		for _, ans := range result.Answer {
			if ans.Type == 1 { // Type 1 is A record (IPv4)
				if net.ParseIP(ans.Data) != nil {
					return ans.Data, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no valid IPv4 address found for %s", host)
}

func isCFIP(host string) (bool, string, error) {
	if contains(CFDOMAN, host) {
		return true, host, nil
	}

	if cf, ok := cache[host]; ok {
		return cf, host, nil
	}

	ip := net.ParseIP(host)
	if ip != nil {
		// IP address (IPv4 or IPv6)
		log.Printf("Checking IP in Cloudflare CIDR for %s", host)
		cf, ip := ipInCFCidr(host)
		cache[host] = cf
		return cf, ip, nil
	} else {
		// Domain name
		log.Printf("DNS lookup for %s", host)
		resolvedIP, err := dns(host)
		if err != nil {
			log.Printf("DNS lookup error: %v", err)
			return false, host, nil
		}
		log.Printf("DNS lookup result: %s", resolvedIP)
		cf, _ := ipInCFCidr(resolvedIP)
		cache[host] = cf
		return cf, resolvedIP, nil
	}
}

// 添加这个自定义的 WebSocket 拨号器
type customDialer struct {
	*websocket.Dialer
}

func (d *customDialer) Dial(urlStr string, requestHeader http.Header) (*websocket.Conn, *http.Response, error) {
	conn, resp, err := d.Dialer.Dial(urlStr, requestHeader)
	if err != nil {
		return nil, resp, err
	}

	conn.SetCloseHandler(func(code int, text string) error {
		return nil
	})

	return conn, resp, nil
}

type wsDialer struct{}

func (d *wsDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}

	isCF, ip, err := isCFIP(host) // Assuming domain name
	if err != nil {
		return nil, err
	}
	if isCF && config.ProxyIP == "" {
		// Direct connection
		log.Printf("Connecting directly to %s:%d", ip, port)
		return net.Dial("tcp", fmt.Sprintf("%s:%d", ip, port))
	}

	// WebSocket connection
	log.Printf("Connecting via WebSocket to %s:%d", host, port)
	url := fmt.Sprintf("wss://%s", config.Domain)
	if config.WKIP != "" {
		url = fmt.Sprintf("wss://%s:%d", config.WKIP, config.WKPort)
		log.Printf("Using WebSocket URL: %s", url)
	}

	dialer := &customDialer{
		Dialer: &websocket.Dialer{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				ServerName:         config.Domain,
			},
			HandshakeTimeout: time.Duration(5+rand.Intn(10)) * time.Second, // Random timeout between 5-15 seconds
		},
	}

	header := http.Header{}
	header.Set("Host", config.Domain)
	header.Set("User-Agent", getRandomUserAgent()) // Add random User-Agent

	wsConn, _, err := dialer.Dial(url, header)
	if err != nil {
		return nil, err
	}

	targetIP := ip
	targetPort := uint16(port)
	if isCF {
		targetIP = config.ProxyIP
		targetPort = uint16(config.ProxyPort)
	}

	message := map[string]interface{}{
		"hostname": targetIP,
		"port":     targetPort,
		"psw":      config.PSW,
	}

	err = wsConn.WriteJSON(message)
	if err != nil {
		wsConn.Close()
		return nil, err
	}

	return &wsConnection{conn: wsConn}, nil
}

type wsConnection struct {
	conn *websocket.Conn
}

func (w *wsConnection) Read(b []byte) (int, error) {
	_, message, err := w.conn.ReadMessage()
	if err != nil {
		return 0, err
	}
	return copy(b, message), nil
}

func (w *wsConnection) Write(b []byte) (int, error) {
	return len(b), w.conn.WriteMessage(websocket.BinaryMessage, b)
}

func (w *wsConnection) Close() error {
	return w.conn.Close()
}

func (w *wsConnection) LocalAddr() net.Addr {
	return w.conn.LocalAddr()
}

func (w *wsConnection) RemoteAddr() net.Addr {
	return w.conn.RemoteAddr()
}

func (w *wsConnection) SetDeadline(t time.Time) error {
	return w.conn.SetReadDeadline(t)
}

func (w *wsConnection) SetReadDeadline(t time.Time) error {
	return w.conn.SetReadDeadline(t)
}

func (w *wsConnection) SetWriteDeadline(t time.Time) error {
	return w.conn.SetWriteDeadline(t)
}

func main() {
	// Read configuration file
	configFile, err := os.ReadFile("config.json")
	if err != nil {
		log.Fatal("Error reading config file:", err)
	}

	err = json.Unmarshal(configFile, &config)
	if err != nil {
		log.Fatal("Error parsing config file:", err)
	}

	// Add custom hostnames to CFDOMAN
	CFDOMAN = append(CFDOMAN, config.CFHS...)

	// Create a SOCKS5 server
	conf := &socks5.Config{
		Dial: (&wsDialer{}).Dial,
	}
	server, err := socks5.New(conf)
	if err != nil {
		log.Fatal(err)
	}

	// Start SOCKS5 server
	log.Printf("SOCKS5 server started on %s:%d\n", config.SBind, config.Sport)
	if err := server.ListenAndServe("tcp", fmt.Sprintf("%s:%d", config.SBind, config.Sport)); err != nil {
		log.Fatal("Error starting server:", err)
	}

	// Seed the random number generator
	rand.Seed(time.Now().UnixNano())
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func getRandomUserAgent() string {
	userAgents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/14.1.1 Safari/605.1.15",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.101 Safari/537.36",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 14_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/14.0 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (iPad; CPU OS 14_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/91.0.4472.80 Mobile/15E148 Safari/604.1",
	}
	return userAgents[rand.Intn(len(userAgents))]
}
