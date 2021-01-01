package camweb

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"sync"

	"github.com/deepch/vdk/av"
)

var config = loadConfig()

type Config struct {
	sync.RWMutex
	Server  ServerSettings            `json:"server"`
	Streams map[string]StreamSettings `json:"streams"`
}

type ServerSettings struct {
	HTTPPort string `json:"http_port"`
}

type StreamSettings struct {
	URL     string `json:"url"`
	Status  bool   `json:"status"`
	Codecs  []av.CodecData
	clients map[string]viewer
}

type viewer struct {
	ch chan av.Packet
}

func loadConfig() *Config {
	var tmp Config
	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		log.Fatalln(err)
	}
	err = json.Unmarshal(data, &tmp)
	if err != nil {
		log.Fatalln(err)
	}
	for i, v := range tmp.Streams {
		v.clients = make(map[string]viewer)
		tmp.Streams[i] = v
	}
	return &tmp
}

func (c *Config) cast(uuid string, pck av.Packet) {
	c.RLock()
	defer c.RUnlock()

	for _, v := range c.Streams[uuid].clients {
		if len(v.ch) < cap(v.ch) {
			v.ch <- pck
		}
	}
}

func (c *Config) exists(suuid string) bool {
	c.RLock()
	defer c.RUnlock()
	_, ok := c.Streams[suuid]
	return ok
}

func (c *Config) setCodec(suuid string, codecs []av.CodecData) {
	c.Lock()
	defer c.Unlock()

	t := c.Streams[suuid]
	t.Codecs = codecs
	c.Streams[suuid] = t
}

func (c *Config) connect(suuid string) (string, chan av.Packet) {
	c.Lock()
	defer c.Unlock()

	cuuid := pseudoUUID()
	ch := make(chan av.Packet, 100)
	c.Streams[suuid].clients[cuuid] = viewer{ch: ch}
	return cuuid, ch
}

func (c *Config) list() (string, []string) {
	c.RLock()
	defer c.RUnlock()

	var res []string
	var first string
	for k := range c.Streams {
		if first == "" {
			first = k
		}
		res = append(res, k)
	}
	return first, res
}

func (c *Config) disconnect(suuid, cuuid string) {
	c.Lock()

	ch := c.Streams[suuid].clients[cuuid].ch
	delete(c.Streams[suuid].clients, cuuid)
	c.Unlock()

	for len(ch) > 0 {
		<-ch
	}
	close(ch)
}

func pseudoUUID() (uuid string) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		fmt.Println("Error: ", err)
		return
	}
	uuid = fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
	return
}
