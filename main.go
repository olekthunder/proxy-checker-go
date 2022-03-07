package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/buaazp/fasthttprouter"
	jsoniter "github.com/json-iterator/go"
	"github.com/pkg/errors"
	"github.com/valyala/fasthttp"
	"golang.org/x/net/proxy"
)

type ProxyConnectionData struct {
	Ip   string
	Port uint32
	Auth *proxy.Auth
}

func (p ProxyConnectionData) Addr() string {
	return fmt.Sprintf("%s:%v", p.Ip, p.Port)
}

func ParseProxy(s string) (ProxyConnectionData, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return ProxyConnectionData{}, errors.WithStack(err)
	}
	port, err := strconv.ParseUint(portStr, 10, 32)
	if err != nil {
		return ProxyConnectionData{}, errors.WithStack(err)
	}
	return ProxyConnectionData{Ip: host, Port: uint32(port)}, nil
}

type ProxyDB interface {
	Add(proxy *ProxyInfo) error
	Proxies() chan *ProxyInfo
	Refresh()
}

type LocalProxyDB struct {
	proxies       map[*ProxyInfo]struct{}
	byCountryCode map[string]map[*ProxyInfo]struct{}
	lock          sync.RWMutex
}

func NewProxyDB() *LocalProxyDB {
	return &LocalProxyDB{
		proxies:       map[*ProxyInfo]struct{}{},
		byCountryCode: map[string]map[*ProxyInfo]struct{}{},
		lock:          sync.RWMutex{},
	}
}

func (pdb *LocalProxyDB) Add(proxy *ProxyInfo) error {
	pdb.lock.Lock()
	defer pdb.lock.Unlock()
	pdb.proxies[proxy] = struct{}{}
	proxies, ok := pdb.byCountryCode[proxy.CountryCode]
	if ok {
		proxies[proxy] = struct{}{}
	} else {
		pdb.byCountryCode[proxy.CountryCode] = map[*ProxyInfo]struct{}{proxy: {}}
	}
	return nil
}

func (pdb *LocalProxyDB) Proxies() chan *ProxyInfo {
	ch := make(chan *ProxyInfo)
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

type ProxyInfo struct {
	proxy       ProxyConnectionData
	CountryCode string
}

func (p *ProxyInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Ip          string `json:"ip"`
		Port        uint32 `json:"port"`
		CountryCode string `json:"countryCode"`
	}{
		Ip:          p.proxy.Ip,
		Port:        p.proxy.Port,
		CountryCode: p.CountryCode,
	})
}

func checkProxy(p ProxyConnectionData) (*ProxyInfo, error) {
	dialer, err := proxy.SOCKS5("tcp", p.Addr(), nil, proxy.Direct)
	if err != nil {
		fmt.Fprintln(os.Stderr, "can't connect to the proxy:", err)
		return nil, errors.WithStack(err)
	}
	httpTransport := &http.Transport{}
	client := &http.Client{Transport: httpTransport, Timeout: 10 * time.Second}
	httpTransport.Dial = dialer.Dial
	resp, err := client.Get("http://ip-api.com/json")
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	r := new(ProxyCheckResponse)
	decoder.Decode(r)
	if r.Query != p.Ip {
		return nil, errors.New("Ip mismatch")
	}
	return &ProxyInfo{proxy: p, CountryCode: r.CountryCode}, nil
}

func getProxies() chan ProxyConnectionData {
	ch := make(chan ProxyConnectionData)
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

func (pdb *LocalProxyDB) Refresh() {
	newDB := NewProxyDB()
	var wg sync.WaitGroup
	for p := range getProxies() {
		wg.Add(1)
		p := p
		go func() {
			proxyInfo, err := checkProxy(p)
			if err != nil {
				// fmt.Println(err)
			} else {
				fmt.Printf("ADDED! %v\n", p)
				newDB.Add(proxyInfo)
			}
			wg.Done()
		}()
	}
	wg.Wait()
	pdb.lock.Lock()
	defer pdb.lock.Unlock()
	pdb.proxies = newDB.proxies
	pdb.byCountryCode = newDB.byCountryCode
}

type JsonHandler struct {
	db ProxyDB
}

func (jh JsonHandler) HandleGet(ctx *fasthttp.RequestCtx) {
	ctx.SetContentType("application/json")
	s := jsoniter.NewStream(jsoniter.ConfigDefault, ctx, 0)
	s.WriteArrayStart()
	ch := jh.db.Proxies()
	p, ok := <-ch
	if ok {
		for {
			s.WriteVal(p)
			p, ok = <-ch
			if ok {
				s.WriteMore()
			} else {
				break
			}
			s.Flush()
		}
	}
	s.WriteArrayEnd()
	s.Flush()
}

func runServer(addr string, db ProxyDB) {
	router := fasthttprouter.New()
	router.GET("/json", JsonHandler{db}.HandleGet)
	fasthttp.ListenAndServe(addr, router.Handler)
}

func main() {
	db := NewProxyDB()
	fmt.Println("Refreshing...")
	go db.Refresh()
	addr := ":8080"
	fmt.Printf("Running at %s\n", addr)
	runServer(addr, db)
}
