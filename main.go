package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/net/proxy"
)

type Proxy struct {
	Ip   string
	Port string
	Auth *proxy.Auth
}

func (p Proxy) Addr() string {
	return fmt.Sprintf("%s:%s", p.Ip, p.Port)
}

func ParseProxy(s string) (Proxy, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return Proxy{}, errors.WithStack(err)
	}
	return Proxy{Ip: host, Port: port}, nil
}

type ProxyDB interface {
	Add(proxy Proxy) error
	Proxies() chan Proxy
}

type LocalProxyDB struct {
	proxies map[Proxy]struct{}
	lock    sync.RWMutex
}

func NewProxyDB() *LocalProxyDB {
	return &LocalProxyDB{
		proxies: map[Proxy]struct{}{},
		lock:    sync.RWMutex{},
	}
}

func (pdb *LocalProxyDB) Add(proxy Proxy) error {
	pdb.lock.Lock()
	defer pdb.lock.Unlock()
	pdb.proxies[proxy] = struct{}{}
	return nil
}

func (pdb *LocalProxyDB) Proxies() chan Proxy {
	ch := make(chan Proxy)
	go func() {
		pdb.lock.RLock()
		defer pdb.lock.RUnlock()
		for proxy := range pdb.proxies {
			ch <- proxy
		}
		close(ch)
	}()
	return ch
}

type ProxyCheckResponse struct {
	Query       string
	CountryCode string
}

func checkProxy(p Proxy) error {
	dialer, err := proxy.SOCKS5("tcp", p.Addr(), nil, proxy.Direct)
	if err != nil {
		fmt.Fprintln(os.Stderr, "can't connect to the proxy:", err)
		return errors.WithStack(err)
	}
	httpTransport := &http.Transport{}
	client := &http.Client{Transport: httpTransport, Timeout: 10 * time.Second}
	httpTransport.Dial = dialer.Dial
	resp, err := client.Get("http://ip-api.com/json")
	if err != nil {
		return errors.WithStack(err)
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	r := new(ProxyCheckResponse)
	decoder.Decode(r)
	if r.Query != p.Ip {
		return errors.New("Ip mismatch")
	}
	return nil
}

func getProxies() chan Proxy {
	ch := make(chan Proxy)
	go func() {
		client := http.Client{Timeout: time.Second * 10}
		resp, err := client.Get("http://localhost:8000/socks5.txt")
		if err != nil {
			panic("NO SOCKS5.txt")
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			p, err := ParseProxy(scanner.Text())
			if err != nil {
				fmt.Println("Invalid proxy")
				return
			}
			ch <- p
		}
		close(ch)
	}()
	return ch
}

func main() {
	db := NewProxyDB()
	var wg sync.WaitGroup
	for p := range getProxies() {
		wg.Add(1)
		p := p
		go func ()  {
			err := checkProxy(p)
			if err != nil {
				// fmt.Println(err)
			} else {
				// fmt.Printf("ADDED! %v\n", p)
				db.Add(p)
			}
			wg.Done()
		}()
	}
	wg.Wait()
	for p := range db.Proxies() {
		fmt.Println(p)
	}
}
