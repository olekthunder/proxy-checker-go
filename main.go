package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/buaazp/fasthttprouter"
	jsoniter "github.com/json-iterator/go"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
	"golang.org/x/net/proxy"
)

var (
	GetProxiesUrl = os.Getenv("GET_PROXIES_URL")
	Addr          = os.Getenv("ADDR")
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
	ByCountryCode(code string) chan *ProxyInfo
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

func (pdb *LocalProxyDB) ByCountryCode(code string) chan *ProxyInfo {
	ch := make(chan *ProxyInfo)
	go func() {
		pdb.lock.RLock()
		defer pdb.lock.RUnlock()
		if proxies, ok := pdb.byCountryCode[code]; ok {
			for proxy := range proxies {
				ch <- proxy
			}
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
	return &ProxyInfo{proxy: p, CountryCode: r.CountryCode}, nil
}

func getProxies() chan ProxyConnectionData {
	ch := make(chan ProxyConnectionData)
	go func() {
		client := http.Client{Timeout: time.Second * 10}
		resp, err := client.Get(GetProxiesUrl)
		if err != nil {
			log.Error("Can't get proxies")
			return
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			p, err := ParseProxy(scanner.Text())
			if err != nil {
				log.Warnf("invalid proxy: %s", p.Addr())
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
				log.Debug(err)
			} else {
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

type ProxyStream chan *ProxyInfo

func (stream ProxyStream) EncodeAsJsonArray(w io.Writer) {
	s := jsoniter.NewStream(jsoniter.ConfigDefault, w, 0)
	s.WriteArrayStart()
	p, ok := <-stream
	if ok {
		for {
			s.WriteVal(p)
			p, ok = <-stream
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

func getProxyStream(db ProxyDB, ctx *fasthttp.RequestCtx) ProxyStream {
	var s ProxyStream
	if code := string(ctx.QueryArgs().Peek("code")); code != "" {
		s = db.ByCountryCode(code)
	} else {
		s = ProxyStream(db.Proxies())
	}
	return s
}

func (stream ProxyStream) EncodeAsTxtList(w io.Writer) {
	for p := range stream {
		w.Write([]byte(p.proxy.Addr() + "\n"))
	}
}

type JsonHandler struct {
	db ProxyDB
}

func (jh JsonHandler) HandleGet(ctx *fasthttp.RequestCtx) {
	ctx.SetContentType("application/json")
	s := getProxyStream(jh.db, ctx)
	s.EncodeAsJsonArray(ctx)
}

type TxtHandler struct {
	db ProxyDB
}

func (th TxtHandler) HandleGet(ctx *fasthttp.RequestCtx) {
	ctx.SetContentType("application/txt")
	s := getProxyStream(th.db, ctx)
	s.EncodeAsTxtList(ctx)
}

func runServer(addr string, db ProxyDB) {
	router := fasthttprouter.New()
	router.GET("/json", JsonHandler{db}.HandleGet)
	router.GET("/proxies.txt", TxtHandler{db}.HandleGet)
	fasthttp.ListenAndServe(addr, router.Handler)
}

func main() {
	log.SetLevel(log.InfoLevel)
	db := NewProxyDB()
	log.Info("Refreshing...")
	go func() {
		db.Refresh()
		time.Sleep(5 * time.Minute)
	}()
	log.Infof("Running at %s\n", Addr)
	runServer(Addr, db)
}
