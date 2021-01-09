package camweb

import (
	"encoding/base64"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/webrtc/v3"
	"log"
	"net/http"
	"net/http/pprof"
	"strings"
	"sync"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func ServeHTTP() {
	router := gin.Default()
	router.GET("/", func(context *gin.Context) {
		context.File("index.html")
	})
	router.GET("/ws", connect)
	router.GET("/streams", func(c *gin.Context) {
		c.JSON(200, config.Streams)
	})
	router.GET("/streams/:id", func(c *gin.Context) {
		if streams, ok := config.Streams[c.Param("id")]; ok {
			c.JSON(200, streams.Codecs)
		} else {
			c.JSON(404, "No codecs found")
		}
	})
	router.POST("/control/:id", func(c *gin.Context) {
		type Command struct {
			Cmd string `json:"cmd" binding:"eq=start|eq=stop"`
			Dir string `json:"dir" binding:"eq=Left|eq=Right|eq=Up|eq=Down"`
		}
		var cmd Command
		if err := c.BindJSON(&cmd); err != nil {
			c.AbortWithStatusJSON(400, "Bad request: "+err.Error())
			return
		}
		if streams, ok := config.Streams[c.Param("id")]; ok {
			url := streams.URL
			if err := control(url, cmd.Cmd, cmd.Dir); err != nil {
				c.AbortWithStatusJSON(500, err.Error())
				return
			}
			c.JSON(200, "OK")
		} else {
			c.JSON(404, "No codecs found")
		}
	})
	{
		debug := router.Group("/debug")
		debug.GET("/pprof/", gin.WrapF(pprof.Index))
		debug.GET("/pprof/cmdline", gin.WrapF(pprof.Cmdline))
		debug.GET("/pprof/profile", gin.WrapF(pprof.Profile))
		debug.GET("/pprof/symbol", gin.WrapF(pprof.Symbol))
		debug.GET("/pprof/goroutine", gin.WrapH(pprof.Handler("goroutine")))
		debug.GET("/pprof/heap", gin.WrapH(pprof.Handler("heap")))
		debug.GET("/pprof/threadcreate", gin.WrapH(pprof.Handler("threadcreate")))
		debug.GET("/pprof/block", gin.WrapH(pprof.Handler("block")))
	}
	fmt.Printf("http://127.0.0.1%v\n", config.Server.HTTPPort)
	err := router.Run(config.Server.HTTPPort)
	if err != nil {
		log.Fatalln("Start HTTP Server error", err)
	}
}

func connect(c *gin.Context) {
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}

	type Payload struct {
		SUUID string `json:"suuid"`
		Data  string `json:"data"`
	}
	var payload Payload
	if err = ws.ReadJSON(&payload); err != nil {
		log.Println(err)
		return
	}

	suuid := payload.SUUID

	stream, ok := config.Streams[suuid]
	if !ok {
		log.Println("stream not found")
		return
	}

	sd, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		log.Println("DecodeString error", err)
		return
	}

	mediaEngine := &webrtc.MediaEngine{}
	if err = mediaEngine.RegisterDefaultCodecs(); err != nil {
		log.Println("RegisterDefaultCodecs error", err)
		return
	}
	registry := &interceptor.Registry{}
	if err = ConfigureNack(mediaEngine, registry); err != nil {
		log.Println("ConfigureNack error", err)
		return
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithInterceptorRegistry(registry))

	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Println("NewPeerConnection error", err)
		return
	}

	var wsWriteLock sync.Mutex
	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		wsWriteLock.Lock()
		defer wsWriteLock.Unlock()
		if err := ws.WriteJSON(candidate.ToJSON()); err != nil {
			log.Println("Failed sending ICE candidate", err)
		} else {
			log.Println("Sent ICE candidate", candidate.ToJSON().Candidate)
		}
	})

	rtpSender, err := peerConnection.AddTrack(stream.VideoTrack)
	if err != nil {
		log.Println("AddTrack error", err)
		return
	}

	// Read incoming RTCP packets
	// Before these packets are returned they are processed by interceptors. For things
	// like NACK this needs to be called.
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	audioRtpSender, err := peerConnection.AddTrack(stream.AudioTrack)
	if err != nil {
		log.Println(err)
		return
	}
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := audioRtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(sd),
	}
	if err := peerConnection.SetRemoteDescription(offer); err != nil {
		log.Println("SetRemoteDescription error", err, offer.SDP)
		return
	}
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Println("CreateAnswer error", err)
		return
	}

	if err = peerConnection.SetLocalDescription(answer); err != nil {
		log.Println("SetLocalDescription error", err)
		return
	}

	go func() {
		for {
			var candidate webrtc.ICECandidateInit
			if err = ws.ReadJSON(&candidate); err != nil {
				if !strings.Contains(err.Error(), "use of closed network connection") {
					log.Println("Error reading candidate", err)
				}
				return
			}
			if err = peerConnection.AddICECandidate(candidate); err != nil {
				log.Println("Failed adding ICE Candidate", err, candidate)
				return
			} else {
				log.Println("Set remote ICE candidate", candidate.Candidate)
			}
		}
	}()

	wsWriteLock.Lock()
	if err = ws.WriteJSON(map[string]string{
		"sdp": peerConnection.LocalDescription().SDP,
	}); err != nil {
		log.Println("Failed sending SDP back", err)
	} else {
		log.Println("Sent SDP")
	}
	wsWriteLock.Unlock()
}

// ConfigureNack will setup everything necessary for handling generating/responding to nack messages.
func ConfigureNack(mediaEngine *webrtc.MediaEngine, interceptorRegistry *interceptor.Registry) error {
	generator, err := nack.NewGeneratorInterceptor()
	if err != nil {
		return err
	}

	responder, err := nack.NewResponderInterceptor()
	if err != nil {
		return err
	}

	mediaEngine.RegisterFeedback(webrtc.RTCPFeedback{Type: "nack"}, webrtc.RTPCodecTypeVideo)
	mediaEngine.RegisterFeedback(webrtc.RTCPFeedback{Type: "nack", Parameter: "pli"}, webrtc.RTPCodecTypeVideo)
	interceptorRegistry.Add(responder)
	interceptorRegistry.Add(generator)
	return nil
}
