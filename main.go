package main

import (
	"fmt"
	"sync"

	"golang.org/x/net/proxy"
)

type Proxy struct {
	addr string
	auth *proxy.Auth
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
	go func ()  {
		pdb.lock.RLock()
		defer pdb.lock.RUnlock()
		for proxy := range pdb.proxies {
			ch <- proxy
		}
		close(ch)
	}()
	return ch
}

func main() {
	db := NewProxyDB()
	db.Add(Proxy{addr: "123"})
	db.Add(Proxy{addr: "345"})
	db.Add(Proxy{addr: "456"})
	for p := range db.Proxies() {
		fmt.Println(p)
	}
}
