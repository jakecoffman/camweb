package camweb

import (
	"log"
	"time"

	"github.com/deepch/vdk/format/rtsp"
)

func ServeStreams() {
	for k, v := range config.Streams {
		go stream(k, v.URL)
	}
}

// stream connects to the camera and starts sending it to any connected clients
func stream(name, url string) {
	for {
		//rtsp.DebugRtsp = true
		session, err := rtsp.Dial(url)
		if err != nil {
			log.Println(name, err)
			time.Sleep(5 * time.Second)
			continue
		}
		session.RtpKeepAliveTimeout = 10 * time.Second
		if err != nil {
			log.Println(name, err)
			time.Sleep(5 * time.Second)
			continue
		}
		codec, err := session.Streams()
		if err != nil {
			log.Println(name, err)
			time.Sleep(5 * time.Second)
			continue
		}
		config.setCodec(name, codec)
		for {
			pkt, err := session.ReadPacket()
			if err != nil {
				log.Println(name, err)
				break
			}
			config.cast(name, pkt)
		}
		if err = session.Teardown(); err != nil {
			log.Println("teardown error", err)
		}
		if err = session.Close(); err != nil {
			log.Println("session Close error", err)
		}
		log.Println(name, "reconnect wait 5s")
		time.Sleep(5 * time.Second)
	}
}
