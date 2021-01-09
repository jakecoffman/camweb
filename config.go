package camweb

import (
	"encoding/json"
	"github.com/pion/webrtc/v3"
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
	URL    string `json:"url"`
	Status bool   `json:"status"`
	Codecs []av.CodecData

	VideoTrack *webrtc.TrackLocalStaticSample
	AudioTrack *webrtc.TrackLocalStaticSample
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
		tmp.Streams[i] = v
	}
	return &tmp
}

func (c *Config) setStream(suuid string, codecs []av.CodecData, videoTrack, audioTrack *webrtc.TrackLocalStaticSample) {
	c.Lock()
	defer c.Unlock()

	t := c.Streams[suuid]
	t.Codecs = codecs
	t.VideoTrack = videoTrack
	t.AudioTrack = audioTrack
	c.Streams[suuid] = t
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
